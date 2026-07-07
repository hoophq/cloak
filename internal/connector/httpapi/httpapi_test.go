package httpapi

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hoophq/cloak/internal/config"
	"github.com/hoophq/cloak/internal/connector"
)

const (
	realKey = "sk-real-do-not-leak-1234567890"
	token   = "sessiontoken123"
)

// fakeUpstream records the credential it received and streams a chunked
// response, standing in for a real HTTP API.
type fakeUpstream struct {
	srv      *httptest.Server
	authHdr  string
	apiKey   string
	path     string
	requests int
}

func newFakeUpstream(t *testing.T) *fakeUpstream {
	f := &fakeUpstream{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.authHdr = r.Header.Get("Authorization")
		f.apiKey = r.Header.Get("x-api-key")
		f.path = r.URL.Path
		f.requests++
		fl, _ := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, "data: one\n\n")
		if fl != nil {
			fl.Flush()
		}
		io.WriteString(w, "data: two\n\n")
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// serve starts the connector against the upstream on a fresh loopback
// listener and returns the base URL the "agent" should call.
func serve(t *testing.T, up *fakeUpstream, auth string) string {
	t.Helper()
	host, portStr, _ := net.SplitHostPort(strings.TrimPrefix(up.srv.URL, "http://"))
	port, _ := strconv.Atoi(portStr)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	sess := connector.Session{
		Upstream: config.Upstream{
			Name: "api", Type: config.TypeHTTP, Host: host, Port: port,
			Auth: auth, TLS: config.TLSDisable,
		},
		Credential: realKey,
		Token:      token,
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		if err := (Connector{}).Serve(ctx, ln, sess); err != nil {
			t.Errorf("Serve: %v", err)
		}
	}()
	return "http://" + ln.Addr().String()
}

// do retries briefly so the test does not race listener startup.
func do(t *testing.T, req *http.Request) *http.Response {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		resp, err := http.DefaultClient.Do(req.Clone(req.Context()))
		if err == nil {
			return resp
		}
		if time.Now().After(deadline) {
			t.Fatalf("request never succeeded: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestBearerInjectionAndStreaming(t *testing.T) {
	up := newFakeUpstream(t)
	base := serve(t, up, config.AuthBearer)

	req, _ := http.NewRequest("GET", base+"/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+connector.FakeKey(token))
	resp := do(t, req)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}
	if up.authHdr != "Bearer "+realKey {
		t.Fatalf("upstream saw Authorization %q, want the real key", up.authHdr)
	}
	if up.path != "/v1/models" {
		t.Fatalf("upstream path = %q", up.path)
	}
	if !strings.Contains(string(body), "data: one") || !strings.Contains(string(body), "data: two") {
		t.Fatalf("streamed body not passed through: %q", body)
	}
}

func TestHeaderAuthInjection(t *testing.T) {
	up := newFakeUpstream(t)
	base := serve(t, up, "header:x-api-key")

	req, _ := http.NewRequest("GET", base+"/v1/messages", nil)
	req.Header.Set("x-api-key", connector.FakeKey(token))
	resp := do(t, req)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if up.apiKey != realKey {
		t.Fatalf("upstream saw x-api-key %q, want the real key", up.apiKey)
	}
	if strings.Contains(up.apiKey, connector.FakeKeyPrefix) {
		t.Fatal("fake key reached the upstream")
	}
}

func TestWrongKeyRejectedBeforeUpstream(t *testing.T) {
	up := newFakeUpstream(t)
	base := serve(t, up, config.AuthBearer)

	req, _ := http.NewRequest("GET", base+"/v1/models", nil)
	req.Header.Set("Authorization", "Bearer cloak-not-the-session-token")
	resp := do(t, req)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
	if up.requests != 0 {
		t.Fatalf("upstream contacted %d times despite bad key", up.requests)
	}
}

func TestMissingKeyRejected(t *testing.T) {
	up := newFakeUpstream(t)
	base := serve(t, up, config.AuthBearer)

	req, _ := http.NewRequest("GET", base+"/v1/models", nil)
	resp := do(t, req)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
	if up.requests != 0 {
		t.Fatalf("upstream contacted despite missing key")
	}
}
