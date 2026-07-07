// Package connector defines the interface protocol connectors implement.
package connector

import (
	"context"
	"net"

	"github.com/hoophq/cloak/internal/config"
)

// FakeKeyPrefix marks HTTP session keys as cloak-issued in transcripts and
// scanner output — recognizable, and worthless off-box or after the session.
const FakeKeyPrefix = "cloak-"

// FakeKey is the per-session API key handed to agents for HTTP upstreams.
func FakeKey(token string) string {
	return FakeKeyPrefix + token
}

// Session carries what a connector needs to serve one upstream for one
// `cloak run`: the upstream definition, the real credential (a Postgres
// password or an HTTP API key/token), and the fake per-session token the
// agent authenticates with.
type Session struct {
	Upstream   config.Upstream
	Credential string
	Token      string
}

// Connector serves client connections accepted on a local listener,
// substituting the real credential on the upstream side. Implementations
// must never write the real credential to the client connection or to logs.
type Connector interface {
	// Serve accepts and handles connections on ln until ctx is canceled or
	// the listener is closed; a closed listener is a clean shutdown, not an
	// error.
	Serve(ctx context.Context, ln net.Listener, sess Session) error
}
