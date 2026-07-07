// Package connector defines the interface protocol connectors implement.
package connector

import (
	"context"
	"net"

	"github.com/hoophq/cloak/internal/config"
)

// Session carries what a connector needs to serve one agent connection: the
// upstream definition, the real credential, and the fake per-session token
// the agent authenticates with.
type Session struct {
	Upstream config.Upstream
	Password string
	Token    string
}

// Connector serves client connections accepted on a local listener,
// substituting the real credential on the upstream side. Implementations
// must never write the real credential to the client connection or to logs.
type Connector interface {
	HandleConn(ctx context.Context, client net.Conn, sess Session) error
}
