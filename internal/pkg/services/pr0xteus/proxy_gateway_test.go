package pr0xteus

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/psyb0t/pr0xteus/internal/pkg/cellproxy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	thingsocks5 "github.com/things-go/go-socks5"
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

	leaseURL, err := url.Parse(lease.URL)
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

func TestCellResolverLeavesDestinationDNSForTheCell(t *testing.T) {
	t.Parallel()

	ctx := context.WithValue(context.Background(), "test-key", "test-value")
	resolvedCtx, address, err := (cellResolver{}).Resolve(ctx, "example.invalid")
	require.NoError(t, err)
	assert.Equal(t, ctx, resolvedCtx)
	assert.Equal(t, net.IPv4zero, address)
}

func gatewayTestManager(t *testing.T, cellSocksAddr, publicAddr string) *Manager {
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
			FailureCacheTTL: time.Minute,
			SpawnTimeout:    time.Second,
			SOCKSPublicAddr: publicAddr,
			ProxyLeaseTTL:   time.Minute,
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
