package cellproxy

import (
	"net"
	"sync"
)

// countingConn wraps a proxied target connection. go-socks5 copies client bytes
// into it (Write = up, toward the destination) and destination bytes out of it
// (Read = down, toward the client), so the two directions map cleanly onto the
// recorder. Close marks the live connection finished exactly once.
type countingConn struct {
	net.Conn

	recorder *Recorder
	key      string
	closed   sync.Once
}

func newCountingConn(conn net.Conn, recorder *Recorder, key string) *countingConn {
	return &countingConn{Conn: conn, recorder: recorder, key: key}
}

// Read counts destination-to-client bytes (down).
func (c *countingConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	if n > 0 {
		c.recorder.Down(c.key, int64(n))
	}

	//nolint:wrapcheck // transparent net.Conn passthrough; wrapping breaks io.Copy's io.EOF handling
	return n, err
}

// Write counts client-to-destination bytes (up).
func (c *countingConn) Write(b []byte) (int, error) {
	n, err := c.Conn.Write(b)
	if n > 0 {
		c.recorder.Up(c.key, int64(n))
	}

	//nolint:wrapcheck // transparent net.Conn passthrough
	return n, err
}

// Close releases the live-connection slot once and closes the underlying conn.
func (c *countingConn) Close() error {
	c.closed.Do(func() {
		c.recorder.Close(c.key)
	})

	//nolint:wrapcheck // transparent net.Conn passthrough
	return c.Conn.Close()
}
