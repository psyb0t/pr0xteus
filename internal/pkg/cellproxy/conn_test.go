package cellproxy

import (
	"io"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCountingConn_CountsBytesAndClosesOnce(t *testing.T) {
	t.Parallel()

	recorder := NewRecorder(10)
	key := recorder.Open("dest:443")

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })

	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}

		_, _ = io.Copy(conn, conn) // echo back whatever it receives
		_ = conn.Close()
	}()

	raw, err := net.Dial("tcp", listener.Addr().String())
	require.NoError(t, err)

	counting := newCountingConn(raw, recorder, key)

	_, err = counting.Write([]byte("hello"))
	require.NoError(t, err)

	buf := make([]byte, 5)
	_, err = io.ReadFull(counting, buf)
	require.NoError(t, err)
	assert.Equal(t, "hello", string(buf))

	snap := recorder.Snapshot()
	assert.Equal(t, int64(5), snap.BytesUp)
	assert.Equal(t, int64(5), snap.BytesDown)
	assert.Equal(t, int64(1), snap.Active)

	require.NoError(t, counting.Close())
	// Second close must be a no-op: the live-connection slot is released once.
	_ = counting.Close()
	assert.Equal(t, int64(0), recorder.Snapshot().Active)
}
