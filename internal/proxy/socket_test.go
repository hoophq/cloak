package proxy

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hoophq/cloak/internal/config"
	"github.com/hoophq/cloak/internal/secret"
)

func TestFakeSocketDSN(t *testing.T) {
	u := config.Upstream{Name: "pg-prod", Type: config.TypePostgres, ListenPort: 5433, Socket: true}
	dsn := FakeSocketDSN(u, "tok123", "/tmp/cloakXYZ")
	// The socket directory is passed via the host query param (slashes escaped),
	// identity/database are fake, and the port keys the socket filename.
	for _, want := range []string{
		"postgres://cloak:tok123@",
		"/pg-prod",
		"host=%2Ftmp%2FcloakXYZ",
		"port=5433",
		"sslmode=disable",
	} {
		if !strings.Contains(dsn, want) {
			t.Errorf("DSN %q missing %q", dsn, want)
		}
	}
	if strings.Contains(dsn, "127.0.0.1") {
		t.Errorf("socket DSN should not contain a TCP host: %s", dsn)
	}
}

func TestSocketListenerBindsAndCleansUp(t *testing.T) {
	u := config.Upstream{
		Name: "pg", Type: config.TypePostgres, Host: "127.0.0.1", Port: 5432,
		Database: "app", User: "app_user", ListenPort: 5455,
		Env: "DATABASE_URL", TLS: config.TLSDisable, Socket: true,
	}
	mgr, err := New([]config.Upstream{u}, secret.Mem{"pg": "pw"})
	if err != nil {
		t.Fatal(err)
	}
	if err := mgr.Start(t.Context()); err != nil {
		t.Fatal(err)
	}

	dsn := mgr.Runtimes[0].FakeURL()
	if !strings.Contains(dsn, "host=") || !strings.Contains(dsn, "sslmode=disable") {
		t.Fatalf("unexpected socket DSN: %s", dsn)
	}

	dir := mgr.socketDir
	if dir == "" {
		t.Fatal("socket directory was not created")
	}
	sock := filepath.Join(dir, fmt.Sprintf(".s.PGSQL.%d", u.ListenPort))
	fi, err := os.Stat(sock)
	if err != nil {
		t.Fatalf("socket not created: %v", err)
	}
	if fi.Mode()&os.ModeSocket == 0 {
		t.Fatalf("%s is not a socket (mode %v)", sock, fi.Mode())
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("socket mode = %o, want 600", perm)
	}

	mgr.Stop()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("socket directory not cleaned up: %v", err)
	}
}
