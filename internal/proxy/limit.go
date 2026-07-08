package proxy

import (
	"net"
	"sync"
)

// maxConnsPerUpstream caps how many client connections one loopback listener
// serves at once. It is generous enough never to interfere with a normal
// client — even a connection pool stays well under it — but bounds resource
// use if a local process opens connections faster than it closes them.
const maxConnsPerUpstream = 256

// limitListener bounds the number of simultaneously open accepted connections.
// At the limit, Accept blocks until an accepted connection is closed. It is a
// small reimplementation of golang.org/x/net/netutil.LimitListener, inlined to
// avoid the dependency.
type limitListener struct {
	net.Listener
	sem chan struct{}
}

func newLimitListener(ln net.Listener, n int) net.Listener {
	return &limitListener{Listener: ln, sem: make(chan struct{}, n)}
}

func (l *limitListener) Accept() (net.Conn, error) {
	l.sem <- struct{}{} // reserve a slot; blocks once n are in use
	conn, err := l.Listener.Accept()
	if err != nil {
		<-l.sem
		return nil, err
	}
	return &limitConn{Conn: conn, release: releaseOnce(l.sem)}, nil
}

// limitConn returns its slot to the listener when closed.
type limitConn struct {
	net.Conn
	release func()
}

func (c *limitConn) Close() error {
	err := c.Conn.Close()
	c.release()
	return err
}

// releaseOnce guards against a double Close returning two slots.
func releaseOnce(sem chan struct{}) func() {
	var once sync.Once
	return func() { once.Do(func() { <-sem }) }
}
