// Package postgres brokers PostgreSQL connections: it accepts the fake
// session credential from the agent, authenticates upstream with the real
// one, then splices bytes transparently for the rest of the session.
package postgres

import (
	"context"
	"crypto/subtle"
	"crypto/tls"
	"fmt"
	"io"
	"maps"
	"net"
	"slices"
	"time"

	"github.com/hoophq/cloak/internal/config"
	"github.com/hoophq/cloak/internal/connector"
	"github.com/hoophq/cloak/internal/pgwire"
)

// FakeUser is the username in every fake DSN; the real upstream username
// never appears in anything the agent sees.
const FakeUser = "cloak"

const (
	dialTimeout      = 10 * time.Second
	handshakeTimeout = 30 * time.Second
)

type Connector struct{}

func (Connector) HandleConn(ctx context.Context, client net.Conn, sess connector.Session) error {
	defer client.Close()
	_ = client.SetDeadline(time.Now().Add(handshakeTimeout))

	// Startup loop: refuse SSL/GSS probes (loopback traffic only ever
	// carries the fake token) until a real startup or cancel arrives.
	var startup *pgwire.Startup
	for startup == nil {
		payload, err := pgwire.ReadStartup(client)
		if err != nil {
			return fmt.Errorf("reading startup: %w", err)
		}
		s, err := pgwire.ParseStartup(payload)
		if err != nil {
			return denyf(client, "08P01", "%v", err)
		}
		switch s.Code {
		case pgwire.SSLRequestCode, pgwire.GSSEncRequestCode:
			if _, err := client.Write([]byte{'N'}); err != nil {
				return err
			}
		case pgwire.CancelRequestCode:
			return forwardCancel(ctx, sess, s)
		default:
			startup = s
		}
	}

	// Verify the fake credential before touching the upstream.
	if startup.Params["user"] != FakeUser {
		return denyf(client, "28P01", "connect as user %q with the DSN cloak provided", FakeUser)
	}
	if err := pgwire.WriteMsg(client, pgwire.CleartextPasswordRequest()); err != nil {
		return err
	}
	msg, err := pgwire.ReadMsg(client)
	if err != nil {
		return fmt.Errorf("reading password: %w", err)
	}
	if msg.Type != pgwire.MsgPassword {
		return denyf(client, "08P01", "expected password message, got %q", msg.Type)
	}
	got := pgwire.CString(msg.Payload)
	if subtle.ConstantTimeCompare([]byte(got), []byte(sess.Token)) != 1 {
		return denyf(client, "28P01", "invalid cloak session token (tokens rotate every `cloak run`)")
	}

	// Authenticate upstream with the real credential.
	up, err := dialAndAuth(ctx, sess, startup.Params)
	if err != nil {
		// Deliberately generic: upstream error text can include the real
		// username, which stays out of agent-visible output.
		_ = denyf(client, "08001", "cloak: upstream connection to %q failed (see cloak logs)", sess.Upstream.Name)
		return fmt.Errorf("upstream %s: %w", sess.Upstream.Name, err)
	}
	defer up.Close()

	// Mirror success, then relay the server greeting (ParameterStatus,
	// BackendKeyData, ...) verbatim until ReadyForQuery.
	if err := pgwire.WriteMsg(client, pgwire.AuthOKMsg()); err != nil {
		return err
	}
	for {
		m, err := pgwire.ReadMsg(up)
		if err != nil {
			return fmt.Errorf("reading upstream greeting: %w", err)
		}
		if m.Type == pgwire.MsgError {
			// The upstream can still refuse post-auth (connection limits,
			// missing database) with messages naming the real user — never
			// relay those verbatim.
			code := pgwire.ErrorCode(m.Payload)
			return denyf(client, code, "cloak: upstream %q refused the session (SQLSTATE %s)", sess.Upstream.Name, code)
		}
		if err := pgwire.WriteMsg(client, m); err != nil {
			return err
		}
		if m.Type == pgwire.MsgReadyForQuery {
			break
		}
	}

	// Handshake done — hand the wires over to a transparent splice.
	_ = client.SetDeadline(time.Time{})
	_ = up.SetDeadline(time.Time{})
	splice(client, up)
	return nil
}

// dialAndAuth connects to the upstream, negotiates TLS per config, and runs
// the real authentication exchange. clientParams (application_name etc.) are
// passed through with the fake identity replaced by the real one.
func dialAndAuth(ctx context.Context, sess connector.Session, clientParams map[string]string) (net.Conn, error) {
	conn, err := dialUpstream(ctx, sess.Upstream)
	if err != nil {
		return nil, err
	}
	_ = conn.SetDeadline(time.Now().Add(handshakeTimeout))

	params := make(map[string]string, len(clientParams))
	maps.Copy(params, clientParams)
	params["user"] = sess.Upstream.User
	// Always route to the configured database: the fake DSN's path is a
	// cosmetic placeholder, and honoring a client-chosen database would
	// widen access beyond what was registered.
	params["database"] = sess.Upstream.DBName()

	if _, err := conn.Write(pgwire.EncodeStartup(params)); err != nil {
		conn.Close()
		return nil, err
	}
	if err := authenticate(conn, sess.Upstream.User, sess.Password); err != nil {
		conn.Close()
		return nil, err
	}
	return conn, nil
}

// authenticate answers the server's authentication requests until
// AuthenticationOk.
func authenticate(conn net.Conn, user, password string) error {
	scram := &scramConversation{user: user, password: password}
	for {
		m, err := pgwire.ReadMsg(conn)
		if err != nil {
			return fmt.Errorf("reading auth response: %w", err)
		}
		switch m.Type {
		case pgwire.MsgError:
			// Upstream auth errors embed the real username; reduce to the
			// SQLSTATE so it never reaches logs the agent might see.
			return fmt.Errorf("upstream authentication failed (SQLSTATE %s)", pgwire.ErrorCode(m.Payload))
		case pgwire.MsgAuth:
			a, err := pgwire.ParseAuth(m.Payload)
			if err != nil {
				return err
			}
			reply, done, err := answerAuth(a, user, password, scram)
			if err != nil {
				return err
			}
			if done {
				return nil
			}
			if reply != nil {
				if err := pgwire.WriteMsg(conn, *reply); err != nil {
					return err
				}
			}
		default:
			return fmt.Errorf("unexpected message %q during authentication", m.Type)
		}
	}
}

func answerAuth(a *pgwire.AuthReq, user, password string, sc *scramConversation) (reply *pgwire.Msg, done bool, err error) {
	switch a.Code {
	case pgwire.AuthOK:
		return nil, true, nil
	case pgwire.AuthCleartextPassword:
		m := pgwire.PasswordMessage(password)
		return &m, false, nil
	case pgwire.AuthMD5Password:
		m := pgwire.PasswordMessage(md5Response(user, password, a.Salt))
		return &m, false, nil
	case pgwire.AuthSASL:
		if !slices.Contains(a.Mechanisms, scramMechanism) {
			return nil, false, fmt.Errorf("server offers %v; cloak only supports %s (channel binding cannot cross a proxy)", a.Mechanisms, scramMechanism)
		}
		first, err := sc.start()
		if err != nil {
			return nil, false, err
		}
		m := pgwire.SASLInitialResponse(scramMechanism, first)
		return &m, false, nil
	case pgwire.AuthSASLContinue:
		resp, err := sc.step(a.Data)
		if err != nil {
			return nil, false, err
		}
		m := pgwire.SASLResponse(resp)
		return &m, false, nil
	case pgwire.AuthSASLFinal:
		if err := sc.finish(a.Data); err != nil {
			return nil, false, err
		}
		return nil, false, nil // AuthenticationOk follows
	default:
		return nil, false, fmt.Errorf("unsupported authentication method %d", a.Code)
	}
}

// dialUpstream opens the TCP connection and negotiates TLS per config.
func dialUpstream(ctx context.Context, u config.Upstream) (net.Conn, error) {
	d := net.Dialer{Timeout: dialTimeout}
	raw, err := d.DialContext(ctx, "tcp", u.Addr())
	if err != nil {
		return nil, err
	}
	if u.TLS == config.TLSDisable {
		return raw, nil
	}
	_ = raw.SetDeadline(time.Now().Add(handshakeTimeout))
	if _, err := raw.Write(pgwire.EncodeSSLRequest()); err != nil {
		raw.Close()
		return nil, err
	}
	var b [1]byte
	if _, err := io.ReadFull(raw, b[:]); err != nil {
		raw.Close()
		return nil, err
	}
	if b[0] != 'S' {
		raw.Close()
		return nil, fmt.Errorf("server does not support TLS; set `tls: disable` only for local development upstreams")
	}
	tc := tls.Client(raw, &tls.Config{ServerName: u.Host})
	if err := tc.HandshakeContext(ctx); err != nil {
		raw.Close()
		return nil, fmt.Errorf("TLS handshake: %w", err)
	}
	_ = raw.SetDeadline(time.Time{})
	return tc, nil
}

// forwardCancel relays a CancelRequest (psql Ctrl+C) to the real upstream.
func forwardCancel(ctx context.Context, sess connector.Session, s *pgwire.Startup) error {
	conn, err := dialUpstream(ctx, sess.Upstream)
	if err != nil {
		return fmt.Errorf("forwarding cancel: %w", err)
	}
	defer conn.Close()
	_, err = conn.Write(pgwire.EncodeCancel(s.CancelPID, s.CancelSecret))
	return err
}

// splice copies bytes in both directions until either side closes.
func splice(a, b net.Conn) {
	done := make(chan struct{}, 2)
	cp := func(dst, src net.Conn) {
		_, _ = io.Copy(dst, src)
		// Unblock the opposite copy; the session is over.
		_ = dst.Close()
		_ = src.Close()
		done <- struct{}{}
	}
	go cp(a, b)
	cp(b, a)
	<-done
}

// denyf sends a FATAL error to the client and returns it as a Go error. The
// message must never contain real credentials or the real username.
func denyf(client net.Conn, sqlstate, format string, args ...any) error {
	err := fmt.Errorf(format, args...)
	_ = pgwire.WriteMsg(client, pgwire.ErrorResponseMsg(sqlstate, err.Error()))
	return err
}
