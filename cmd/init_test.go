package cmd

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/hoophq/cloak/internal/claudecfg"
	"github.com/hoophq/cloak/internal/config"
)

// TestSyncClaudeOnlyWhenInstalled verifies the resync never opts a user in: it
// touches a settings file only where `cloak init` already installed cloak, and
// then writes the new upstream's fake values into it.
func TestSyncClaudeOnlyWhenInstalled(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())

	cfg := &config.Config{Upstreams: []config.Upstream{{
		Name: "openai", Type: config.TypeHTTP, Host: "api.openai.com", Port: 443,
		Auth: config.AuthBearer, ListenPort: 5433,
		Env: "OPENAI_API_KEY", EnvURL: "OPENAI_BASE_URL", TLS: config.TLSVerifyFull,
	}}}

	// Nobody opted in → syncClaude writes nothing.
	refreshed, err := syncClaude(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(refreshed) != 0 {
		t.Fatalf("refreshed without install = %v, want none", refreshed)
	}

	// Opt in the global settings; a later credential should then resync it.
	gpath, err := settingsPath(false)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := claudecfg.Install(gpath, claudecfg.Managed{HookCommand: "/x/cloak _hook"}); err != nil {
		t.Fatal(err)
	}
	refreshed, err = syncClaude(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(refreshed) != 1 || refreshed[0] != gpath {
		t.Fatalf("refreshed = %v, want [%s]", refreshed, gpath)
	}

	// The upstream's fake values are now in the settings file.
	data, err := os.ReadFile(gpath)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Env map[string]string `json:"env"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(doc.Env["OPENAI_API_KEY"], "cloak-") {
		t.Fatalf("OPENAI_API_KEY not resynced: %q", doc.Env["OPENAI_API_KEY"])
	}
	if !strings.HasPrefix(doc.Env["OPENAI_BASE_URL"], "http://127.0.0.1:") {
		t.Fatalf("OPENAI_BASE_URL not resynced: %q", doc.Env["OPENAI_BASE_URL"])
	}
}
