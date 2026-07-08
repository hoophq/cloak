// Package httpapi brokers HTTP APIs: the agent calls a loopback base URL
// with a fake per-session key, and the real credential is injected into the
// configured header on the way out over verified TLS.
package httpapi

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/hoophq/cloak/internal/config"
	"github.com/hoophq/cloak/internal/connector"
)

type Connector struct{}

func (Connector) Serve(ctx context.Context, ln net.Listener, sess connector.Session) error {
	u := sess.Upstream
	header, prefix, err := config.ParseAuth(u.Auth)
	if err != nil {
		return err
	}
	scheme := "https"
	if u.TLS == config.TLSDisable {
		scheme = "http"
	}
	target := &url.URL{Scheme: scheme, Host: u.Addr()}
	fakeKey := connector.FakeKey(sess.Token)

	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.SetXForwarded()
			// The fake key was already validated; swap in the real one.
			pr.Out.Header.Set(header, prefix+sess.Credential)
		},
		// Flush immediately so SSE / streaming responses are not buffered.
		FlushInterval: -1,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			slog.Error("upstream error", "upstream", u.Name, "err", err)
			writeJSONError(w, http.StatusBadGateway,
				fmt.Sprintf("cloak: upstream %q request failed (see cloak logs)", u.Name))
		},
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := strings.TrimPrefix(r.Header.Get(header), prefix)
		if subtle.ConstantTimeCompare([]byte(got), []byte(fakeKey)) != 1 {
			writeJSONError(w, http.StatusUnauthorized,
				"cloak: invalid session key (keys rotate every `cloak run`)")
			return
		}
		proxy.ServeHTTP(w, r)
	})

	srv := &http.Server{
		Handler: handler,
		// The agent process is semi-untrusted; a slow-header client must not
		// pin the listener open, and idle keep-alive connections must not
		// accumulate. No WriteTimeout: it would cut off long streaming (SSE)
		// responses, which are a first-class case here.
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	// The session ends when the manager closes the listener (Serve then
	// returns net.ErrClosed); ctx cancellation is a backstop for callers
	// that don't. Requests in flight at that point are dropped — acceptable
	// because the wrapped agent has already exited.
	defer context.AfterFunc(ctx, func() { _ = srv.Close() })()

	err = srv.Serve(ln)
	if errors.Is(err, http.ErrServerClosed) || errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	fmt.Fprintf(w, "{\"error\":%q}\n", msg)
}
