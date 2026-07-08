package claudecfg

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readDoc(t *testing.T, path string) map[string]json.RawMessage {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("settings not valid JSON: %v\n%s", err, data)
	}
	return doc
}

func sampleManaged() Managed {
	return Managed{
		Env: map[string]string{
			"DATABASE_URL":    "postgres://cloak:tok@127.0.0.1:5433/pg?sslmode=disable",
			"OPENAI_API_KEY":  "cloak-tok",
			"OPENAI_BASE_URL": "http://127.0.0.1:5434",
		},
		HookCommand: "/usr/local/bin/cloak _hook",
	}
}

func TestInstallCreatesEnvAndHooks(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".claude", "settings.json")
	managed, skipped, err := Install(path, sampleManaged())
	if err != nil {
		t.Fatal(err)
	}
	if len(skipped) != 0 {
		t.Fatalf("unexpected skipped: %v", skipped)
	}
	if len(managed) != 3 {
		t.Fatalf("managed = %v, want 3 keys", managed)
	}

	doc := readDoc(t, path)
	var env map[string]string
	json.Unmarshal(doc["env"], &env)
	if env["OPENAI_API_KEY"] != "cloak-tok" {
		t.Fatalf("env not written: %v", env)
	}
	var hooks map[string][]hookGroup
	json.Unmarshal(doc["hooks"], &hooks)
	if len(hooks["SessionStart"]) != 2 || len(hooks["SessionEnd"]) != 1 {
		t.Fatalf("hooks not written as expected: %+v", hooks)
	}
	if got := hooks["SessionEnd"][0].Hooks[0].Command; got != "/usr/local/bin/cloak _hook session-end" {
		t.Fatalf("session-end command = %q", got)
	}
}

func TestInstallPreservesOtherSettingsAndHooks(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	initial := `{
  "model": "sonnet",
  "permissions": {"allow": ["Bash(ls:*)"]},
  "hooks": {
    "PreToolUse": [{"matcher": "Bash", "hooks": [{"type": "command", "command": "/my/guard.sh", "args": ["x"]}]}],
    "SessionStart": [{"matcher": "startup", "hooks": [{"type": "command", "command": "/my/banner.sh"}]}]
  }
}`
	if err := os.WriteFile(path, []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Install(path, sampleManaged()); err != nil {
		t.Fatal(err)
	}

	doc := readDoc(t, path)
	if _, ok := doc["model"]; !ok {
		t.Fatal("lost the model setting")
	}
	if _, ok := doc["permissions"]; !ok {
		t.Fatal("lost the permissions setting")
	}
	// The user's PreToolUse guard and their SessionStart banner must survive,
	// including the guard's custom "args" field.
	raw, _ := json.Marshal(doc["hooks"])
	s := string(raw)
	for _, want := range []string{"/my/guard.sh", `"args"`, "/my/banner.sh", "session-start", "session-end"} {
		if !strings.Contains(s, want) {
			t.Fatalf("hooks missing %q after install:\n%s", want, s)
		}
	}
}

func TestInstallSkipsUserRealEnvValue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	os.WriteFile(path, []byte(`{"env":{"OPENAI_API_KEY":"sk-real-user-key"}}`), 0o644)

	managed, skipped, err := Install(path, sampleManaged())
	if err != nil {
		t.Fatal(err)
	}
	if len(skipped) != 1 || skipped[0] != "OPENAI_API_KEY" {
		t.Fatalf("skipped = %v, want [OPENAI_API_KEY]", skipped)
	}
	if strings.Contains(strings.Join(managed, ","), "OPENAI_API_KEY") {
		t.Fatalf("OPENAI_API_KEY should not be managed: %v", managed)
	}
	doc := readDoc(t, path)
	var env map[string]string
	json.Unmarshal(doc["env"], &env)
	if env["OPENAI_API_KEY"] != "sk-real-user-key" {
		t.Fatalf("clobbered the user's real key: %q", env["OPENAI_API_KEY"])
	}
}

func TestInstallIdempotentAndHealsStalePath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if _, _, err := Install(path, sampleManaged()); err != nil {
		t.Fatal(err)
	}
	// Re-install with a new binary path (simulating an upgrade).
	m := sampleManaged()
	m.HookCommand = "/opt/homebrew/bin/cloak _hook"
	if _, _, err := Install(path, m); err != nil {
		t.Fatal(err)
	}

	doc := readDoc(t, path)
	var hooks map[string][]hookGroup
	json.Unmarshal(doc["hooks"], &hooks)
	if len(hooks["SessionStart"]) != 2 {
		t.Fatalf("duplicate/incorrect SessionStart groups: %d", len(hooks["SessionStart"]))
	}
	if got := hooks["SessionEnd"][0].Hooks[0].Command; got != "/opt/homebrew/bin/cloak _hook session-end" {
		t.Fatalf("stale path not healed: %q", got)
	}
}

func TestUninstallRemovesOnlyCloak(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	initial := `{
  "env": {"OPENAI_API_KEY": "sk-real", "MY_VAR": "keep"},
  "hooks": {"PreToolUse": [{"matcher": "Bash", "hooks": [{"type": "command", "command": "/my/guard.sh"}]}]}
}`
	os.WriteFile(path, []byte(initial), 0o644)

	// A real user key is present, so install skips it (nothing cloak-managed on
	// that key); add a db upstream so cloak manages DATABASE_URL.
	if _, _, err := Install(path, sampleManaged()); err != nil {
		t.Fatal(err)
	}
	if err := Uninstall(path); err != nil {
		t.Fatal(err)
	}

	doc := readDoc(t, path)
	var env map[string]string
	json.Unmarshal(doc["env"], &env)
	if env["OPENAI_API_KEY"] != "sk-real" {
		t.Fatalf("removed the user's real key: %v", env)
	}
	if env["MY_VAR"] != "keep" {
		t.Fatalf("removed an unrelated env var: %v", env)
	}
	if _, ok := env["DATABASE_URL"]; ok {
		t.Fatalf("cloak's fake DATABASE_URL survived uninstall: %v", env)
	}
	var hooks map[string][]hookGroup
	json.Unmarshal(doc["hooks"], &hooks)
	if len(hooks["SessionStart"]) != 0 || len(hooks["SessionEnd"]) != 0 {
		t.Fatalf("cloak hooks survived: %+v", hooks)
	}
	if len(hooks["PreToolUse"]) != 1 {
		t.Fatalf("removed the user's PreToolUse hook: %+v", hooks)
	}
}

func TestUninstallMissingFileIsNoOp(t *testing.T) {
	if err := Uninstall(filepath.Join(t.TempDir(), "nope.json")); err != nil {
		t.Fatalf("uninstall on missing file should be a no-op, got %v", err)
	}
}
