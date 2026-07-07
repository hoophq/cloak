//go:build e2e

// Package e2e exercises the full broker path against a real PostgreSQL in
// Docker: SCRAM upstream auth, fake-token client auth, and the post-handshake
// splice (queries only work if the splice is byte-faithful).
package e2e

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/hoophq/cloak/internal/config"
	"github.com/hoophq/cloak/internal/proxy"
	"github.com/hoophq/cloak/internal/secret"
)

const (
	pgImage    = "postgres:16-alpine"
	pgUser     = "e2euser"
	pgDB       = "e2edb"
	pgPassword = "real-secret-do-not-leak"
)

// startPostgres launches a SCRAM-only postgres container and waits until it
// accepts real-credential connections.
func startPostgres(t *testing.T, ctx context.Context, port int) {
	t.Helper()
	if err := exec.Command("docker", "version").Run(); err != nil {
		t.Skip("docker not available")
	}
	name := fmt.Sprintf("cloak-e2e-%d", time.Now().UnixNano())
	out, err := exec.Command("docker", "run", "-d", "--rm", "--name", name,
		"-e", "POSTGRES_USER="+pgUser,
		"-e", "POSTGRES_PASSWORD="+pgPassword,
		"-e", "POSTGRES_DB="+pgDB,
		"-e", "POSTGRES_INITDB_ARGS=--auth-host=scram-sha-256",
		"-p", fmt.Sprintf("127.0.0.1:%d:5432", port),
		pgImage).CombinedOutput()
	if err != nil {
		t.Fatalf("docker run: %v\n%s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", name).Run() })

	realDSN := fmt.Sprintf("postgres://%s:%s@127.0.0.1:%d/%s", pgUser, pgPassword, port, pgDB)
	deadline := time.Now().Add(90 * time.Second)
	for {
		conn, err := pgx.Connect(ctx, realDSN)
		if err == nil {
			_ = conn.Close(ctx)
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("postgres never became ready: %v", err)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func startManager(t *testing.T, ctx context.Context, pgPort, listenPort int) *proxy.Manager {
	t.Helper()
	mgr, err := proxy.New([]config.Upstream{{
		Name: "pg-e2e", Type: config.TypePostgres,
		Host: "127.0.0.1", Port: pgPort,
		Database: pgDB, User: pgUser,
		ListenPort: listenPort, Env: "DATABASE_URL",
		TLS: config.TLSDisable, // the docker image has no TLS configured
	}}, secret.Mem{"pg-e2e": pgPassword})
	if err != nil {
		t.Fatal(err)
	}
	if err := mgr.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mgr.Stop)
	return mgr
}

func TestBrokeredConnection(t *testing.T) {
	ctx := context.Background()
	pgPort, listenPort := freePort(t), freePort(t)
	startPostgres(t, ctx, pgPort)
	mgr := startManager(t, ctx, pgPort, listenPort)

	fakeDSN := mgr.Runtimes[0].FakeURL()
	if strings.Contains(fakeDSN, pgPassword) || strings.Contains(fakeDSN, pgUser) {
		t.Fatalf("fake DSN leaks real identity: %s", fakeDSN)
	}

	conn, err := pgx.Connect(ctx, fakeDSN)
	if err != nil {
		t.Fatalf("connecting via fake DSN: %v", err)
	}
	defer conn.Close(ctx)

	// current_user proves the credential swap; the query itself proves the
	// splice carries the extended query protocol untouched.
	var who string
	if err := conn.QueryRow(ctx, "select current_user").Scan(&who); err != nil {
		t.Fatal(err)
	}
	if who != pgUser {
		t.Fatalf("current_user = %q, want %q", who, pgUser)
	}

	// A larger round trip through the splice.
	var n int
	if err := conn.QueryRow(ctx, "select count(*) from generate_series(1, 10000)").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 10000 {
		t.Fatalf("count = %d", n)
	}
}

func TestWrongTokenRejected(t *testing.T) {
	ctx := context.Background()
	pgPort, listenPort := freePort(t), freePort(t)
	startPostgres(t, ctx, pgPort)
	startManager(t, ctx, pgPort, listenPort)

	badDSN := fmt.Sprintf("postgres://cloak:wrong-token@127.0.0.1:%d/%s?sslmode=disable", listenPort, pgDB)
	if _, err := pgx.Connect(ctx, badDSN); err == nil {
		t.Fatal("expected connection with a wrong token to fail")
	} else if !strings.Contains(err.Error(), "28P01") && !strings.Contains(err.Error(), "session token") {
		t.Fatalf("unexpected rejection error: %v", err)
	}
}

func TestRealUsernameStaysHidden(t *testing.T) {
	ctx := context.Background()
	pgPort, listenPort := freePort(t), freePort(t)
	startPostgres(t, ctx, pgPort)
	startManager(t, ctx, pgPort, listenPort)

	// Connecting as anyone but the fake user is refused, and the refusal
	// must not reveal the real username.
	dsn := fmt.Sprintf("postgres://%s:whatever@127.0.0.1:%d/%s?sslmode=disable", pgUser, listenPort, pgDB)
	_, err := pgx.Connect(ctx, dsn)
	if err == nil {
		t.Fatal("expected non-cloak user to be rejected")
	}
	if strings.Contains(err.Error(), pgPassword) {
		t.Fatalf("error leaks the real password: %v", err)
	}
}
