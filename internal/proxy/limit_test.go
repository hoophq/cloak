package proxy

import (
	"net"
	"testing"
	"time"
)

// TestLimitListenerCapsConns verifies that a limitListener with capacity 2
// does not accept a third connection until one of the first two is closed,
// and that closing frees the slot.
func TestLimitListenerCapsConns(t *testing.T) {
	base, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ln := newLimitListener(base, 2)
	defer ln.Close()

	accepted := make(chan net.Conn, 8)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			accepted <- c
		}
	}()

	dial := func() net.Conn {
		c, err := net.Dial("tcp", ln.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = c.Close() })
		return c
	}
	dial()
	dial()
	dial() // third: connects at the TCP layer, but must not be Accept()ed yet

	a1 := recvConn(t, accepted)
	recvConn(t, accepted)

	// With two slots in use, the third acceptance must not happen.
	select {
	case <-accepted:
		t.Fatal("third connection accepted despite the cap of 2")
	case <-time.After(150 * time.Millisecond):
	}

	// Closing an accepted connection frees a slot; the third is now accepted.
	if err := a1.Close(); err != nil {
		t.Fatal(err)
	}
	recvConn(t, accepted)
}

func recvConn(t *testing.T, ch <-chan net.Conn) net.Conn {
	t.Helper()
	select {
	case c := <-ch:
		t.Cleanup(func() { _ = c.Close() })
		return c
	case <-time.After(2 * time.Second):
		t.Fatal("expected a connection to be accepted")
		return nil
	}
}
