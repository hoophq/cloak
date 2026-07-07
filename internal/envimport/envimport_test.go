package envimport

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hoophq/cloak/internal/config"
)

const sampleEnv = `# app config
DATABASE_URL='postgres://app_user:s3cret@db.internal:6432/app?sslmode=require'
export ANALYTICS_URL=postgresql://reader:qu3ry@warehouse/insights?sslmode=disable
LOCAL_URL=postgres://cloak:managed-by-cloak@127.0.0.1:5433/local?sslmode=disable
NO_PASSWORD_URL=postgres://svc@db.internal/app
REDIS_URL=redis://:redispw@cache.internal:6379/0
OPENAI_API_KEY=sk-abcdef1234567890
DEBUG=true
PASSWORD_MIN=8
`

func scanSample(t *testing.T) ([]Candidate, []Warning, []string) {
	t.Helper()
	lines := strings.Split(sampleEnv, "\n")
	cands, warns := Scan(lines)
	return cands, warns, lines
}

func TestScanCandidates(t *testing.T) {
	cands, _, _ := scanSample(t)
	if len(cands) != 2 {
		t.Fatalf("candidates = %d, want 2: %+v", len(cands), cands)
	}

	db := cands[0]
	if db.Key != "DATABASE_URL" || db.Password != "s3cret" || db.Export {
		t.Fatalf("first candidate = %+v", db)
	}
	want := config.Upstream{
		Type: config.TypePostgres, Host: "db.internal", Port: 6432,
		Database: "app", User: "app_user", TLS: config.TLSVerifyFull,
	}
	if db.Upstream != want {
		t.Fatalf("upstream = %+v, want %+v", db.Upstream, want)
	}

	an := cands[1]
	if an.Key != "ANALYTICS_URL" || !an.Export || an.Upstream.TLS != config.TLSDisable ||
		an.Upstream.Port != 5432 || an.Upstream.User != "reader" {
		t.Fatalf("second candidate = %+v", an)
	}
}

func TestScanWarnings(t *testing.T) {
	_, warns, _ := scanSample(t)
	got := map[string]bool{}
	for _, w := range warns {
		got[w.Key] = true
	}
	for _, key := range []string{"NO_PASSWORD_URL", "REDIS_URL", "OPENAI_API_KEY"} {
		if !got[key] {
			t.Errorf("expected warning for %s, warnings: %+v", key, warns)
		}
	}
	// Cloak placeholders and boring values must not warn.
	for _, key := range []string{"LOCAL_URL", "DEBUG", "PASSWORD_MIN"} {
		if got[key] {
			t.Errorf("unexpected warning for %s", key)
		}
	}
}

func TestRewrite(t *testing.T) {
	cands, _, lines := scanSample(t)
	for i := range cands {
		cands[i].Upstream.Name = DeriveName(cands[i].Key, func(string) bool { return false })
		cands[i].Upstream.ListenPort = 5433 + i
	}
	out := Rewrite(lines, cands, func(c Candidate) string {
		return "postgres://cloak:managed-by-cloak@127.0.0.1/" + c.Upstream.Name
	})
	text := strings.Join(out, "\n")

	if strings.Contains(text, "s3cret") || strings.Contains(text, "qu3ry") {
		t.Fatalf("rewritten file still contains a real password:\n%s", text)
	}
	if !strings.Contains(text, "DATABASE_URL=postgres://cloak:") ||
		!strings.Contains(text, "export ANALYTICS_URL=postgres://cloak:") {
		t.Fatalf("expected placeholder entries (export preserved):\n%s", text)
	}
	if strings.Count(text, ManagedComment) != 2 {
		t.Fatalf("expected 2 managed comments:\n%s", text)
	}
	// Untouched lines survive verbatim, including the unrelated warning ones.
	for _, keep := range []string{"# app config", "REDIS_URL=redis://:redispw@cache.internal:6379/0", "DEBUG=true"} {
		if !strings.Contains(text, keep) {
			t.Fatalf("lost line %q:\n%s", keep, text)
		}
	}

	// A rewritten file has nothing left to import.
	again, _ := Scan(out)
	if len(again) != 0 {
		t.Fatalf("rewritten file still has candidates: %+v", again)
	}
}

func TestDeriveName(t *testing.T) {
	taken := map[string]bool{"database-url": true, "database-url-2": true}
	if got := DeriveName("DATABASE_URL", func(n string) bool { return taken[n] }); got != "database-url-3" {
		t.Fatalf("DeriveName with collisions = %q", got)
	}
	if got := DeriveName("__WEIRD__KEY__", func(string) bool { return false }); got != "weird-key" {
		t.Fatalf("DeriveName sanitization = %q", got)
	}
}

func TestBackupAndUndo(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	if err := os.WriteFile(path, []byte("A=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := LatestBackup(path); err == nil {
		t.Fatal("expected error when no backup exists")
	}
	first, err := Backup(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("A=2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	second, err := Backup(path)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("backups must not collide")
	}

	latest, err := LatestBackup(path)
	if err != nil {
		t.Fatal(err)
	}
	if latest != second {
		t.Fatalf("latest = %s, want %s", latest, second)
	}
	data, err := os.ReadFile(latest)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "A=2\n" {
		t.Fatalf("latest backup content = %q", data)
	}
}
