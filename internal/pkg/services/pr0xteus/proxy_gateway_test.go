package pr0xteus

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/psyb0t/pr0xteus/internal/pkg/cellproxy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	thingsocks5 "github.com/things-go/go-socks5"
	"github.com/things-go/go-socks5/statute"
	"golang.org/x/net/proxy"
)

func TestProxyGateway_ProxiesThroughLeasedCell(t *testing.T) {
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "controller-gateway-success")
	}))
	t.Cleanup(destination.Close)

	cellSocksAddr := freeTCPAddr(t)
	cellControlAddr := freeTCPAddr(t)
	cell := cellproxy.New(cellproxy.Config{
		CellID:          "cell-gateway-test",
		SOCKSNetwork:    "tcp",
		SOCKSAddr:       cellSocksAddr,
		ControlAddr:     cellControlAddr,
		DialTimeout:     time.Second,
		TopDestinations: 10,
	})
	cellCtx, cancelCell := context.WithCancel(context.Background())
	t.Cleanup(cancelCell)
	cellErr := make(chan error, 1)
	go func() { cellErr <- cell.Run(cellCtx) }()
	waitForTCPListener(t, cellSocksAddr)

	gatewayAddr := freeTCPAddr(t)
	manager := gatewayTestManager(t, cellSocksAddr, gatewayAddr)
	gatewayCtx, cancelGateway := context.WithCancel(context.Background())
	t.Cleanup(cancelGateway)
	gateway := NewProxyGateway(manager, gatewayAddr)
	gatewayErr := make(chan error, 1)
	require.NoError(t, gateway.Start(gatewayCtx, gatewayErr))
	t.Cleanup(func() { _ = gateway.Close() })
	waitForTCPListener(t, gatewayAddr)

	allocation, err := manager.AcquireForPool(context.Background(), "western", nil, false)
	require.NoError(t, err)
	lease, err := manager.IssueLease(allocation)
	require.NoError(t, err)
	manager.Release(allocation)

	leaseURL, err := url.Parse(lease.Proxies.SOCKS5)
	require.NoError(t, err)
	password, ok := leaseURL.User.Password()
	require.True(t, ok)

	dialer, err := proxy.SOCKS5("tcp", leaseURL.Host, &proxy.Auth{
		User:     leaseURL.User.Username(),
		Password: password,
	}, proxy.Direct)
	require.NoError(t, err)
	contextDialer, ok := dialer.(proxy.ContextDialer)
	require.True(t, ok)

	transport := &http.Transport{DialContext: contextDialer.DialContext}
	client := &http.Client{Transport: transport}
	request, err := http.NewRequestWithContext(
		context.Background(), http.MethodGet, destination.URL, nil,
	)
	require.NoError(t, err)
	request.Close = true
	response, err := client.Do(request)
	require.NoError(t, err)
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	transport.CloseIdleConnections()
	assert.Equal(t, "controller-gateway-success", string(body))

	require.Eventually(t, func() bool {
		tunnel := manager.Pools()["western"].Snapshot()

		return tunnel != nil && tunnel.InFlight == 0
	}, time.Second, 10*time.Millisecond)
	assert.Empty(t, gatewayErr)

	cancelCell()
	select {
	case err := <-cellErr:
		require.NoError(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("cell proxy did not stop")
	}
}

func TestProxyGateway_RejectsUnknownLease(t *testing.T) {
	gatewayAddr := freeTCPAddr(t)
	manager := gatewayTestManager(t, "127.0.0.1:1", gatewayAddr)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	gateway := NewProxyGateway(manager, gatewayAddr)
	errorsOut := make(chan error, 1)
	require.NoError(t, gateway.Start(ctx, errorsOut))
	t.Cleanup(func() { _ = gateway.Close() })
	waitForTCPListener(t, gatewayAddr)

	dialer, err := proxy.SOCKS5("tcp", gatewayAddr, &proxy.Auth{
		User: "not-a-lease", Password: "wrong",
	}, proxy.Direct)
	require.NoError(t, err)

	_, err = dialer.Dial("tcp", "127.0.0.1:1")
	require.Error(t, err)
	assert.Empty(t, errorsOut)
}

func TestProxyGateway_DialRequiresLeaseAuthentication(t *testing.T) {
	t.Parallel()

	gateway := NewProxyGateway(gatewayTestManager(t, "127.0.0.1:1", "127.0.0.1:1080"), "127.0.0.1:1080")

	connection, err := gateway.dial(
		context.Background(), "tcp", "example.invalid:443", &thingsocks5.Request{},
	)
	require.Error(t, err)
	assert.Nil(t, connection)
}

func TestProxyGateway_PreservesDestinationAddressTypeForCell(t *testing.T) {
	testCases := []struct {
		name     string
		dest     string
		wantType byte
		wantHost string
	}{
		{
			name:     "domain is resolved by cell",
			dest:     "remote-dns.example.invalid:443",
			wantType: statute.ATYPDomain,
			wantHost: "remote-dns.example.invalid",
		},
		{
			name:     "ipv4 remains ipv4",
			dest:     "192.0.2.10:443",
			wantType: statute.ATYPIPv4,
			wantHost: "192.0.2.10",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			cellSocksAddr, requests := startRecordingSOCKS5Upstream(t)
			gatewayAddr := freeTCPAddr(t)
			manager := gatewayTestManager(t, cellSocksAddr, gatewayAddr)
			gatewayCtx, cancelGateway := context.WithCancel(context.Background())
			t.Cleanup(cancelGateway)

			gateway := NewProxyGateway(manager, gatewayAddr)
			errorsOut := make(chan error, 1)
			require.NoError(t, gateway.Start(gatewayCtx, errorsOut))
			t.Cleanup(func() { require.NoError(t, gateway.Close()) })
			waitForTCPListener(t, gatewayAddr)

			lease := issueGatewayTestLease(t, manager)
			dialer := gatewayLeaseDialer(t, lease)
			conn, err := dialer.DialContext(context.Background(), "tcp", tc.dest)
			require.NoError(t, err)
			require.NoError(t, conn.Close())

			request := awaitSOCKS5Request(t, requests)
			require.NoError(t, request.err)
			assert.Equal(t, tc.wantType, request.addressType)
			assert.Equal(t, tc.wantHost, request.host)
			assert.Equal(t, uint16(443), request.port)

			require.Eventually(t, func() bool {
				tunnel := manager.Pools()["western"].Snapshot()

				return tunnel != nil && tunnel.InFlight == 0
			}, time.Second, 10*time.Millisecond)
			assert.Empty(t, errorsOut)
		})
	}
}

func TestCellResolverLeavesDestinationDNSForTheCell(t *testing.T) {
	t.Parallel()

	ctx := context.WithValue(context.Background(), "test-key", "test-value")
	resolvedCtx, address, err := (cellResolver{}).Resolve(ctx, "example.invalid")
	require.NoError(t, err)
	assert.Equal(t, ctx, resolvedCtx)
	assert.Nil(t, address)
}

func TestProxyGatewaySocksLogger_LogsSafeFailureReason(t *testing.T) {
	var output bytes.Buffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })

	newProxyGatewaySocksLogger(context.Background()).Errorf(
		"server: connect to %s failed", "secret-destination.example",
	)

	var record map[string]any
	require.NoError(t, json.Unmarshal(output.Bytes(), &record))
	assert.Equal(t, "WARN", record["level"])
	assert.Equal(t, "controller SOCKS5 relay failed", record["msg"])
	assert.Equal(t, string(socks5FailureUpstreamConnect), record["reason"])
	assert.NotContains(t, output.String(), "secret-destination.example")
}

func TestProxyGateway_LogsSafeUpstreamFailure(t *testing.T) {
	var output synchronizedBuffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })

	gatewayAddr := freeTCPAddr(t)
	manager := gatewayTestManager(t, "127.0.0.1:1", gatewayAddr)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	gateway := NewProxyGateway(manager, gatewayAddr)
	errorsOut := make(chan error, 1)
	require.NoError(t, gateway.Start(ctx, errorsOut))
	t.Cleanup(func() { require.NoError(t, gateway.Close()) })
	waitForTCPListener(t, gatewayAddr)

	lease := issueGatewayTestLease(t, manager)
	_, err := gatewayLeaseDialer(t, lease).DialContext(
		context.Background(), "tcp", "secret-destination.example:443",
	)
	require.Error(t, err)

	require.Eventually(t, func() bool {
		return strings.Contains(output.String(), "controller SOCKS5 relay failed")
	}, time.Second, 10*time.Millisecond)
	assert.Contains(t, output.String(), `"reason":"upstream_connect_failed"`)
	assert.NotContains(t, output.String(), "secret-destination.example")
	assert.Empty(t, errorsOut)
}

type synchronizedBuffer struct {
	mu sync.Mutex
	bytes.Buffer
}

func (b *synchronizedBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.Buffer.Write(data)
}

func (b *synchronizedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.Buffer.String()
}

func TestClassifySOCKS5Failure(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name   string
		format string
		args   []any
		want   socks5FailureReason
	}{
		{
			name:   "client disconnected",
			format: "server: %v",
			args:   []any{io.EOF},
			want:   socks5FailureClientDisconnected,
		},
		{
			name:   "authentication failed",
			format: "server: failed to authenticate",
			want:   socks5FailureAuthenticationFailed,
		},
		{
			name:   "upstream connect failed",
			format: "server: connect to %s failed",
			args:   []any{"example.invalid:443"},
			want:   socks5FailureUpstreamConnect,
		},
		{
			name:   "unknown protocol error",
			format: "server: malformed request",
			want:   socks5FailureProtocolError,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.want, classifySOCKS5Failure(tc.format, tc.args...))
		})
	}
}

type recordedSOCKS5Request struct {
	addressType byte
	host        string
	port        uint16
	err         error
}

func startRecordingSOCKS5Upstream(t *testing.T) (string, <-chan recordedSOCKS5Request) {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, listener.Close()) })

	requests := make(chan recordedSOCKS5Request, 1)
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			requests <- recordedSOCKS5Request{err: acceptErr}

			return
		}
		defer conn.Close()

		if deadlineErr := conn.SetDeadline(time.Now().Add(3 * time.Second)); deadlineErr != nil {
			requests <- recordedSOCKS5Request{err: deadlineErr}

			return
		}

		request, requestErr := readSOCKS5ConnectRequest(conn)
		if requestErr != nil {
			requests <- recordedSOCKS5Request{err: requestErr}

			return
		}

		if _, writeErr := conn.Write([]byte{0x05, 0x00, 0x00, statute.ATYPIPv4, 0, 0, 0, 0, 0, 0}); writeErr != nil {
			requests <- recordedSOCKS5Request{err: writeErr}

			return
		}

		requests <- request
	}()

	return listener.Addr().String(), requests
}

func readSOCKS5ConnectRequest(conn net.Conn) (recordedSOCKS5Request, error) {
	greeting := make([]byte, 2)
	if _, err := io.ReadFull(conn, greeting); err != nil {
		return recordedSOCKS5Request{}, err
	}

	methods := make([]byte, greeting[1])
	if _, err := io.ReadFull(conn, methods); err != nil {
		return recordedSOCKS5Request{}, err
	}

	if _, err := conn.Write([]byte{0x05, 0x00}); err != nil {
		return recordedSOCKS5Request{}, err
	}

	header := make([]byte, 4)
	if _, err := io.ReadFull(conn, header); err != nil {
		return recordedSOCKS5Request{}, err
	}

	request := recordedSOCKS5Request{addressType: header[3]}
	var err error
	request.host, err = readSOCKS5Host(conn, request.addressType)
	if err != nil {
		return recordedSOCKS5Request{}, err
	}

	var port [2]byte
	if _, err := io.ReadFull(conn, port[:]); err != nil {
		return recordedSOCKS5Request{}, err
	}
	request.port = binary.BigEndian.Uint16(port[:])

	return request, nil
}

func readSOCKS5Host(conn net.Conn, addressType byte) (string, error) {
	switch addressType {
	case statute.ATYPIPv4:
		address := make([]byte, net.IPv4len)
		if _, err := io.ReadFull(conn, address); err != nil {
			return "", err
		}

		return net.IP(address).String(), nil
	case statute.ATYPDomain:
		var length [1]byte
		if _, err := io.ReadFull(conn, length[:]); err != nil {
			return "", err
		}

		address := make([]byte, length[0])
		if _, err := io.ReadFull(conn, address); err != nil {
			return "", err
		}

		return string(address), nil
	default:
		return "", nil
	}
}

func awaitSOCKS5Request(
	t *testing.T, requests <-chan recordedSOCKS5Request,
) recordedSOCKS5Request {
	t.Helper()

	select {
	case request := <-requests:
		return request
	case <-time.After(3 * time.Second):
		t.Fatal("upstream SOCKS5 server did not receive a request")

		return recordedSOCKS5Request{}
	}
}

func issueGatewayTestLease(t *testing.T, manager *Manager) ProxyLease {
	t.Helper()

	allocation, err := manager.AcquireForPool(context.Background(), "western", nil, false)
	require.NoError(t, err)
	lease, err := manager.IssueLease(allocation)
	require.NoError(t, err)
	manager.Release(allocation)

	return lease
}

func gatewayLeaseDialer(t *testing.T, lease ProxyLease) proxy.ContextDialer {
	t.Helper()

	leaseURL, err := url.Parse(lease.Proxies.SOCKS5)
	require.NoError(t, err)
	password, ok := leaseURL.User.Password()
	require.True(t, ok)

	dialer, err := proxy.SOCKS5("tcp", leaseURL.Host, &proxy.Auth{
		User:     leaseURL.User.Username(),
		Password: password,
	}, proxy.Direct)
	require.NoError(t, err)

	contextDialer, ok := dialer.(proxy.ContextDialer)
	require.True(t, ok)

	return contextDialer
}

func gatewayTestManager(t *testing.T, cellSocksAddr, publicAddr string) *Manager {
	return gatewayTestManagerWithPublicAddresses(
		t,
		cellSocksAddr,
		publicAddr,
		defaultHTTPProxyPublicAddr,
	)
}

func gatewayTestManagerWithPublicAddresses(
	t *testing.T, cellSocksAddr, socksPublicAddr, httpProxyPublicAddr string,
) *Manager {
	t.Helper()

	internalURL, err := url.Parse(proxySchemeSOCKS5 + "://" + cellSocksAddr)
	require.NoError(t, err)

	spec := PoolSpec{
		Name:          "western",
		Configs:       []string{"de-frankfurt"},
		ExitCountries: map[string]string{"de-frankfurt": "DE"},
	}
	manager := NewManager(
		Config{
			FailureCacheTTL:     time.Minute,
			SpawnTimeout:        time.Second,
			SOCKSPublicAddr:     socksPublicAddr,
			HTTPProxyPublicAddr: httpProxyPublicAddr,
			ProxyLeaseTTL:       time.Minute,
		},
		map[string]PoolSpec{"western": spec},
		&Router{countryToPool: map[string]string{"de": "western"}},
		&cellsTestSpawner{},
	)
	now := time.Now()
	manager.Pools()["western"].setTunnel(&Tunnel{
		ContainerID: "cell-gateway-test",
		ConfName:    "de-frankfurt",
		ProxyURL:    internalURL,
		GatewayAddr: cellSocksAddr,
		State:       TunnelStateHot,
		Pool:        "western",
		ExitCountry: "DE",
		SpawnedAt:   now,
		HealthyAt:   now,
		LastUsedAt:  now,
	})

	return manager
}

func freeTCPAddr(t *testing.T) string {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	address := listener.Addr().String()
	require.NoError(t, listener.Close())

	return address
}

func waitForTCPListener(t *testing.T, address string) {
	t.Helper()

	require.Eventually(t, func() bool {
		connection, err := net.DialTimeout("tcp", address, 100*time.Millisecond)
		if err != nil {
			return false
		}

		_ = connection.Close()

		return true
	}, 3*time.Second, 20*time.Millisecond)
}
