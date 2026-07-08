// Package proxy binds the local listeners and dispatches accepted
// connections to protocol connectors.
package proxy

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"

	"github.com/hoophq/cloak/internal/config"
	"github.com/hoophq/cloak/internal/connector"
	"github.com/hoophq/cloak/internal/connector/httpapi"
	"github.com/hoophq/cloak/internal/connector/postgres"
	"github.com/hoophq/cloak/internal/secret"
	"github.com/hoophq/cloak/internal/token"
)

var connectors = map[string]connector.Connector{
	config.TypePostgres: postgres.Connector{},
	config.TypeHTTP:     httpapi.Connector{},
}

// Runtime is one upstream prepared for a session: real credential loaded,
// fake token generated, listener (once started) bound.
type Runtime struct {
	Session connector.Session
	ln      net.Listener
	// socketDir holds this upstream's unix socket when it uses socket mode;
	// set at Start time and used to build the fake DSN.
	socketDir string
}

// PlaceholderToken is the token `cloak import` writes into files. It can
// never match a real session token, so a file-resident DSN fails closed at
// the proxy instead of granting or leaking anything.
const PlaceholderToken = "managed-by-cloak"

// FakeDSN builds the loopback DSN for an upstream with the given token. It
// contains only fake identity and the loopback address. The path segment is
// cosmetic — the connector always routes to the configured database — so it
// carries the cloak-local upstream name, never a real identifier.
func FakeDSN(u config.Upstream, token string) string {
	return fmt.Sprintf("postgres://%s:%s@127.0.0.1:%d/%s?sslmode=disable",
		postgres.FakeUser, token, u.ListenPort, url.PathEscape(u.Name))
}

// FakeURL is the DSN handed to the agent for this session.
func (r *Runtime) FakeURL() string {
	if r.Session.Upstream.Socket {
		return FakeSocketDSN(r.Session.Upstream, r.Session.Token, r.socketDir)
	}
	return FakeDSN(r.Session.Upstream, r.Session.Token)
}

// FakeSocketDSN builds the loopback DSN for a unix-socket upstream: the agent
// connects through the socket in dir. Identity and database are fake or
// cosmetic, exactly as in FakeDSN.
func FakeSocketDSN(u config.Upstream, token, dir string) string {
	q := url.Values{}
	q.Set("host", dir)
	q.Set("port", strconv.Itoa(u.ListenPort))
	q.Set("sslmode", "disable")
	fake := &url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(postgres.FakeUser, token),
		Path:     "/" + u.Name,
		RawQuery: q.Encode(),
	}
	return fake.String()
}

// EnvAssignments returns the NAME=VALUE pairs injected into the wrapped
// command for this upstream. Every value is fake or loopback-local.
func (r *Runtime) EnvAssignments() []string {
	u := r.Session.Upstream
	switch u.Type {
	case config.TypeHTTP:
		vars := []string{u.Env + "=" + connector.FakeKey(r.Session.Token)}
		if u.EnvURL != "" {
			vars = append(vars, fmt.Sprintf("%s=http://127.0.0.1:%d", u.EnvURL, u.ListenPort))
		}
		return vars
	default:
		return []string{u.Env + "=" + r.FakeURL()}
	}
}

// Manager owns the listeners for one `cloak run` session.
type Manager struct {
	Runtimes []*Runtime
	// socketDir is the per-session directory holding unix sockets (0700),
	// created lazily when an upstream uses socket mode.
	socketDir string
}

// New loads credentials and mints a fresh token per upstream. It fails fast
// (before any listener binds) if a credential is missing.
func New(upstreams []config.Upstream, store secret.Store) (*Manager, error) {
	m := &Manager{}
	for _, u := range upstreams {
		if err := u.Validate(); err != nil {
			return nil, err
		}
		pw, err := store.Get(u.Name)
		if err != nil {
			return nil, err
		}
		tok, err := token.New()
		if err != nil {
			return nil, err
		}
		m.Runtimes = append(m.Runtimes, &Runtime{
			Session: connector.Session{Upstream: u, Credential: pw, Token: tok},
		})
	}
	return m, nil
}

// Start binds all listeners and begins serving.
func (m *Manager) Start(ctx context.Context) error {
	for _, r := range m.Runtimes {
		ln, err := m.listen(r)
		if err != nil {
			m.Stop()
			return err
		}
		// Bound concurrent connections so a misbehaving local client cannot
		// exhaust file descriptors or goroutines.
		r.ln = newLimitListener(ln, maxConnsPerUpstream)
		go m.serve(ctx, r)
	}
	return nil
}

// listen binds one runtime's local listener: loopback TCP by default, or a
// unix-domain socket in a private 0700 directory when the upstream opts into
// socket mode (so only this user can reach it).
func (m *Manager) listen(r *Runtime) (net.Listener, error) {
	u := r.Session.Upstream
	if !u.Socket {
		addr := fmt.Sprintf("127.0.0.1:%d", u.ListenPort)
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			return nil, fmt.Errorf("binding %s for %q: %w", addr, u.Name, err)
		}
		return ln, nil
	}
	if err := m.ensureSocketDir(); err != nil {
		return nil, err
	}
	// libpq/pgx locate a unix socket as <host>/.s.PGSQL.<port>.
	path := filepath.Join(m.socketDir, fmt.Sprintf(".s.PGSQL.%d", u.ListenPort))
	if len(path) > 100 {
		return nil, fmt.Errorf("unix socket path too long for %q (%d chars): %s", u.Name, len(path), path)
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("binding unix socket for %q: %w", u.Name, err)
	}
	// The 0700 directory already restricts access to this user; tighten the
	// socket itself to 0600 as defense in depth.
	if err := os.Chmod(path, 0o600); err != nil {
		_ = ln.Close()
		return nil, err
	}
	r.socketDir = m.socketDir
	return ln, nil
}

// ensureSocketDir lazily creates the per-session directory holding unix
// sockets. os.MkdirTemp creates it 0700.
func (m *Manager) ensureSocketDir() error {
	if m.socketDir != "" {
		return nil
	}
	dir, err := os.MkdirTemp("", "cloak")
	if err != nil {
		return fmt.Errorf("creating socket directory: %w", err)
	}
	m.socketDir = dir
	return nil
}

func (m *Manager) serve(ctx context.Context, r *Runtime) {
	name := r.Session.Upstream.Name
	c, ok := connectors[r.Session.Upstream.Type]
	if !ok { // unreachable: Validate rejects unknown types
		slog.Error("no connector for type", "upstream", name, "type", r.Session.Upstream.Type)
		return
	}
	if err := c.Serve(ctx, r.ln, r.Session); err != nil {
		slog.Error("listener error", "upstream", name, "err", err)
	}
}

// Stop closes all listeners. In-flight connections die with the process,
// which exits right after the wrapped agent does.
func (m *Manager) Stop() {
	for _, r := range m.Runtimes {
		if r.ln != nil {
			_ = r.ln.Close() // closing a unix listener unlinks its socket file
		}
	}
	if m.socketDir != "" {
		_ = os.RemoveAll(m.socketDir)
	}
}
