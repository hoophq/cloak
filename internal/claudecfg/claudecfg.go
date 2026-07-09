// Package claudecfg installs and removes cloak's entries in a Claude Code
// settings.json — an `env` block that hands the agent fake credentials and
// SessionStart/SessionEnd hooks that drive the proxy — without disturbing any
// other setting the user has. It is the mechanism behind `cloak init`, letting
// plain `claude` run through cloak with no `cloak run` wrapper.
package claudecfg

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

// hookMarker identifies cloak's hook commands so init can converge (heal a
// stale binary path) and uninstall can remove exactly its own entries.
const hookMarker = "_hook session-"

// Managed is what cloak installs into a settings file.
type Managed struct {
	Env         map[string]string // fake env values, keyed by variable name
	HookCommand string            // absolute invocation; " session-start" / " session-end" are appended
}

// isFake reports whether v is a value cloak wrote, so uninstall never deletes a
// real credential and install never clobbers one.
func isFake(v string) bool {
	return strings.HasPrefix(v, "cloak-") ||
		strings.HasPrefix(v, "postgres://cloak:") ||
		strings.HasPrefix(v, "http://127.0.0.1:")
}

type hookEntry struct {
	Type    string `json:"type"`
	Command string `json:"command"`
}

type hookGroup struct {
	Matcher string      `json:"matcher,omitempty"`
	Hooks   []hookEntry `json:"hooks"`
}

// Install merges cloak's env block and hooks into the settings file at path
// (creating it and its directory if absent), preserving every other setting.
// It is idempotent: previous cloak entries are converged, not duplicated. It
// returns the env keys it now manages and any it skipped because the user had
// already set them to a non-cloak value.
func Install(path string, m Managed) (managed, skipped []string, err error) {
	doc, err := load(path)
	if err != nil {
		return nil, nil, err
	}

	env, err := decodeEnv(doc)
	if err != nil {
		return nil, nil, err
	}
	for k, v := range m.Env {
		if cur, ok := env[k]; ok && !isFake(cur) {
			skipped = append(skipped, k) // leave the user's real value untouched
			continue
		}
		env[k] = v
		managed = append(managed, k)
	}
	if err := encodeEnv(doc, env); err != nil {
		return nil, nil, err
	}

	if err := setHooks(doc, m.HookCommand); err != nil {
		return nil, nil, err
	}

	sort.Strings(managed)
	sort.Strings(skipped)
	return managed, skipped, save(path, doc)
}

// Installed reports whether cloak's integration is present in the settings file
// at path — i.e. `cloak init` has run against it. A missing file means not
// installed, not an error. Callers use this to resync only the files a user
// opted in, never to create one.
func Installed(path string) (bool, error) {
	doc, err := load(path)
	if err != nil {
		return false, err
	}
	hooks, err := decodeHooks(doc)
	if err != nil {
		return false, err
	}
	for _, groups := range hooks {
		if slices.ContainsFunc(groups, isCloakGroup) {
			return true, nil
		}
	}
	return false, nil
}

// Uninstall removes exactly cloak's hooks and the env keys it set (only where
// the value is still a cloak fake), then cleans up any container it empties.
// Removing when nothing is installed is a no-op.
func Uninstall(path string) error {
	doc, err := load(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	env, err := decodeEnv(doc)
	if err != nil {
		return err
	}
	for k, v := range env {
		if isFake(v) {
			delete(env, k)
		}
	}
	if err := encodeEnv(doc, env); err != nil {
		return err
	}

	if err := removeHooks(doc); err != nil {
		return err
	}
	return save(path, doc)
}

// --- settings document helpers -------------------------------------------
//
// The document is kept as top-level raw JSON so untouched settings survive
// byte-for-byte; only `env` and cloak's own hook groups are decoded.

func load(path string) (map[string]json.RawMessage, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]json.RawMessage{}, nil
	}
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return map[string]json.RawMessage{}, nil
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	return doc, nil
}

func save(path string, doc map[string]json.RawMessage) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	out = append(out, '\n')
	perm := os.FileMode(0o644)
	if fi, err := os.Stat(path); err == nil {
		perm = fi.Mode().Perm()
	}
	return os.WriteFile(path, out, perm)
}

func decodeEnv(doc map[string]json.RawMessage) (map[string]string, error) {
	env := map[string]string{}
	if raw, ok := doc["env"]; ok {
		if err := json.Unmarshal(raw, &env); err != nil {
			return nil, fmt.Errorf("parsing env: %w", err)
		}
	}
	return env, nil
}

func encodeEnv(doc map[string]json.RawMessage, env map[string]string) error {
	if len(env) == 0 {
		delete(doc, "env")
		return nil
	}
	raw, err := json.Marshal(env)
	if err != nil {
		return err
	}
	doc["env"] = raw
	return nil
}

// hookEvents are the events cloak manages; SessionStart is matched only on real
// session beginnings, SessionEnd on any termination.
var hookEvents = []struct {
	event, matcher string
	arg            string
}{
	{"SessionStart", "startup", "session-start"},
	{"SessionStart", "resume", "session-start"},
	{"SessionEnd", "", "session-end"},
}

// setHooks removes any existing cloak hook groups (heal), then adds the current
// ones, leaving all of the user's hooks intact.
func setHooks(doc map[string]json.RawMessage, command string) error {
	hooks, err := decodeHooks(doc)
	if err != nil {
		return err
	}
	// Strip prior cloak groups once per event, before adding any — otherwise a
	// second managed group on the same event would strip the first.
	stripped := map[string]bool{}
	for _, h := range hookEvents {
		if !stripped[h.event] {
			hooks[h.event] = stripCloak(hooks[h.event])
			stripped[h.event] = true
		}
	}
	for _, h := range hookEvents {
		g := hookGroup{Matcher: h.matcher, Hooks: []hookEntry{{Type: "command", Command: command + " " + h.arg}}}
		raw, err := json.Marshal(g)
		if err != nil {
			return err
		}
		hooks[h.event] = append(hooks[h.event], raw)
	}
	return encodeHooks(doc, hooks)
}

func removeHooks(doc map[string]json.RawMessage) error {
	hooks, err := decodeHooks(doc)
	if err != nil {
		return err
	}
	for event, groups := range hooks {
		kept := stripCloak(groups)
		if len(kept) == 0 {
			delete(hooks, event)
		} else {
			hooks[event] = kept
		}
	}
	return encodeHooks(doc, hooks)
}

func decodeHooks(doc map[string]json.RawMessage) (map[string][]json.RawMessage, error) {
	hooks := map[string][]json.RawMessage{}
	if raw, ok := doc["hooks"]; ok {
		if err := json.Unmarshal(raw, &hooks); err != nil {
			return nil, fmt.Errorf("parsing hooks: %w", err)
		}
	}
	return hooks, nil
}

func encodeHooks(doc map[string]json.RawMessage, hooks map[string][]json.RawMessage) error {
	if len(hooks) == 0 {
		delete(doc, "hooks")
		return nil
	}
	raw, err := json.Marshal(hooks)
	if err != nil {
		return err
	}
	doc["hooks"] = raw
	return nil
}

// stripCloak returns the matcher groups that are not cloak's, preserving each
// user group's raw JSON (and any fields cloak does not model).
func stripCloak(groups []json.RawMessage) []json.RawMessage {
	kept := groups[:0:0] // fresh slice, never alias the input
	for _, raw := range groups {
		if isCloakGroup(raw) {
			continue
		}
		kept = append(kept, raw)
	}
	return kept
}

func isCloakGroup(raw json.RawMessage) bool {
	var g struct {
		Hooks []struct {
			Command string `json:"command"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(raw, &g); err != nil {
		return false
	}
	for _, h := range g.Hooks {
		if strings.Contains(h.Command, hookMarker) {
			return true
		}
	}
	return false
}
