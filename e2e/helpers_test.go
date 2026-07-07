//go:build e2e

package e2e

import (
	"net"
	"testing"
)

// freePort reserves an ephemeral port, then releases it so a listener can
// rebind it. Racy in theory, fine for tests on a quiet machine.
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}
