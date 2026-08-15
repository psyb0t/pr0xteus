package cellproxy

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/proxy"
)

// freeAddr returns a currently-free loopback address for a listener to bind.
func freeAddr(t *testing.T) string {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	addr := listener.Addr().String()
	require.NoError(t, listener.Close())

	return addr
}

func TestNew_BuildsServer(t *testing.T) {
	t.Parallel()

	server := New(Config{TopDestinations: 5})
	require.NotNil(t, server)
	require.NotNil(t, server.recorder)
	require.NotNil(t, server.socks)
}

func TestServer_DialRecordsSuccessfulConnection(t *testing.T) {
	t.Parallel()

	dest, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = dest.Close() })

	go func() {
		for {
			conn, acceptErr := dest.Accept()
			if acceptErr != nil {
				return
			}

			_ = conn.Close()
		}
	}()

	server := New(Config{TopDestinations: 5})
	dialFn := server.dial(&net.Dialer{Timeout: time.Second})

	conn, err := dialFn(context.Background(), "tcp", dest.Addr().String())
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	_, isCounting := conn.(*countingConn)
	assert.True(t, isCounting, "successful dial returns a metrics-counting conn")

	snap := server.recorder.Snapshot()
	assert.Equal(t, int64(1), snap.Requests)
	assert.Equal(t, int64(1), snap.Active)
}

func TestServer_DialFailureIsRecorded(t *testing.T) {
	t.Parallel()

	server := New(Config{TopDestinations: 5})
	dialFn := server.dial(&net.Dialer{Timeout: 200 * time.Millisecond})

	_, err := dialFn(context.Background(), "tcp", "127.0.0.1:1")
	require.Error(t, err)
	assert.Equal(t, int64(1), server.recorder.Snapshot().DialFailures)
}

func TestServer_ControlMuxServesHealthAndStatus(t *testing.T) {
	t.Parallel()

	server := New(Config{CellID: "cell-x", ParentID: "ctrl-y", TopDestinations: 5})
	mux := server.controlMux()

	healthResp := httptest.NewRecorder()
	mux.ServeHTTP(healthResp, httptest.NewRequest(http.MethodGet, pathHealthz, nil))
	assert.Equal(t, http.StatusOK, healthResp.Code)
	assert.Contains(t, healthResp.Body.String(), "ok")

	statusResp := httptest.NewRecorder()
	mux.ServeHTTP(statusResp, httptest.NewRequest(http.MethodGet, pathStatus, nil))
	assert.Equal(t, http.StatusOK, statusResp.Code)

	var status Status
	require.NoError(t, json.NewDecoder(statusResp.Body).Decode(&status))
	assert.Equal(t, "cell-x", status.CellID)
	assert.Equal(t, "ctrl-y", status.ParentID)
}

func TestSocksLogger_DoesNotPanic(t *testing.T) {
	t.Parallel()

	assert.NotPanics(t, func() {
		socksLogger{}.Errorf("connect failed: %d", 1)
	})
}

// TestServer_EndToEndProxiesAndRecords drives the whole proxy in-process: a real
// SOCKS5 client dials a real destination THROUGH cellproxy, and the control
// server's /status must then report the request and the bytes that flowed.
func TestServer_EndToEndProxiesAndRecords(t *testing.T) {
	dest := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, "hello-through-cellproxy")
		},
	))
	t.Cleanup(dest.Close)

	socksAddr := freeAddr(t)
	controlAddr := freeAddr(t)

	server := New(Config{
		CellID:          "cell-e2e",
		ParentID:        "ctrl-e2e",
		SOCKSNetwork:    "tcp",
		SOCKSAddr:       socksAddr,
		ControlAddr:     controlAddr,
		DialTimeout:     5 * time.Second,
		TopDestinations: 10,
	})

	ctx, cancel := context.WithCancel(context.Background())

	runErr := make(chan error, 1)
	go func() { runErr <- server.Run(ctx) }()

	require.Eventually(t, func() bool {
		conn, err := net.DialTimeout("tcp", socksAddr, 200*time.Millisecond)
		if err != nil {
			return false
		}

		_ = conn.Close()

		return true
	}, 3*time.Second, 50*time.Millisecond, "socks listener should come up")

	dialer, err := proxy.SOCKS5("tcp", socksAddr, nil, proxy.Direct)
	require.NoError(t, err)

	contextDialer, ok := dialer.(proxy.ContextDialer)
	require.True(t, ok)

	client := &http.Client{
		Transport: &http.Transport{DialContext: contextDialer.DialContext},
	}

	resp, err := client.Get(dest.URL)
	require.NoError(t, err)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	assert.Equal(t, "hello-through-cellproxy", string(body))

	statusResp, err := http.Get("http://" + controlAddr + pathStatus)
	require.NoError(t, err)

	var status Status
	require.NoError(t, json.NewDecoder(statusResp.Body).Decode(&status))
	require.NoError(t, statusResp.Body.Close())

	assert.Equal(t, "cell-e2e", status.CellID)
	assert.Positive(t, status.Traffic.Requests)
	assert.Positive(t, status.Traffic.BytesUp)
	assert.Positive(t, status.Traffic.BytesDown)
	require.NotEmpty(t, status.Traffic.Destinations)

	healthResp, err := http.Get("http://" + controlAddr + pathHealthz)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, healthResp.StatusCode)
	require.NoError(t, healthResp.Body.Close())

	cancel()

	select {
	case err := <-runErr:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after context cancel")
	}
}
