// Package proxy binds the local listeners and dispatches accepted
// connections to protocol connectors.
package proxy

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/url"

	"github.com/hoophq/cloak/internal/config"
	"github.com/hoophq/cloak/internal/connector"
	"github.com/hoophq/cloak/internal/connector/postgres"
	"github.com/hoophq/cloak/internal/secret"
	"github.com/hoophq/cloak/internal/token"
)

var connectors = map[string]connector.Connector{
	config.TypePostgres: postgres.Connector{},
}

// Runtime is one upstream prepared for a session: real credential loaded,
// fake token generated, listener (once started) bound.
type Runtime struct {
	Session connector.Session
	ln      net.Listener
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
	return FakeDSN(r.Session.Upstream, r.Session.Token)
}

// EnvVar is the environment variable the fake DSN is injected as.
func (r *Runtime) EnvVar() string {
	return r.Session.Upstream.Env
}

// Manager owns the listeners for one `cloak run` session.
type Manager struct {
	Runtimes []*Runtime
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
			Session: connector.Session{Upstream: u, Password: pw, Token: tok},
		})
	}
	return m, nil
}

// Start binds all listeners on loopback and begins serving.
func (m *Manager) Start(ctx context.Context) error {
	for _, r := range m.Runtimes {
		addr := fmt.Sprintf("127.0.0.1:%d", r.Session.Upstream.ListenPort)
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			m.Stop()
			return fmt.Errorf("binding %s for %q: %w", addr, r.Session.Upstream.Name, err)
		}
		r.ln = ln
		go m.serve(ctx, r)
	}
	return nil
}

func (m *Manager) serve(ctx context.Context, r *Runtime) {
	name := r.Session.Upstream.Name
	c, ok := connectors[r.Session.Upstream.Type]
	if !ok { // unreachable: Validate rejects unknown types
		slog.Error("no connector for type", "upstream", name, "type", r.Session.Upstream.Type)
		return
	}
	for {
		conn, err := r.ln.Accept()
		if err != nil {
			return // listener closed
		}
		go func() {
			if err := c.HandleConn(ctx, conn, r.Session); err != nil {
				slog.Error("session error", "upstream", name, "err", err)
			}
		}()
	}
}

// Stop closes all listeners. In-flight connections die with the process,
// which exits right after the wrapped agent does.
func (m *Manager) Stop() {
	for _, r := range m.Runtimes {
		if r.ln != nil {
			_ = r.ln.Close()
		}
	}
}
