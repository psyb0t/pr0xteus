package client

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClient_PreflightComparesDirectAndSOCKS5Egress(t *testing.T) {
	t.Parallel()

	directEcho := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, err := w.Write([]byte("198.51.100.10\n"))
		require.NoError(t, err)
	}))
	t.Cleanup(directEcho.Close)

	proxyEcho := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, err := w.Write([]byte("203.0.113.20\n"))
		require.NoError(t, err)
	}))
	t.Cleanup(proxyEcho.Close)

	proxyURL, err := url.Parse("socks5://" + newTestSOCKS5Relay(t))
	require.NoError(t, err)

	pool := &scriptedPool{results: []scriptedPoolResult{{
		response: &ProxyResponse{ProxyURL: proxyURL},
	}}}
	client, err := New(
		clientTestCountry,
		pool,
		WithDirectTransport(clientTestDirectTransportFactory(t, directEcho.URL)),
		WithIPEchoEndpoints([]string{proxyEcho.URL + "/one", proxyEcho.URL + "/two"}),
	)
	require.NoError(t, err)

	require.NoError(t, client.PreflightSanityCheck(context.Background()))
	require.Len(t, pool.recordedRequests(), 1)
	assert.Equal(t, clientTestCountry, pool.recordedRequests()[0].Country)
	assert.False(t, pool.recordedRequests()[0].FallbackOK)
}

func TestClient_PreflightRejectsAProxyThatLeaksTheDirectIP(t *testing.T) {
	t.Parallel()

	echo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, err := w.Write([]byte("198.51.100.10\n"))
		require.NoError(t, err)
	}))
	t.Cleanup(echo.Close)

	proxyURL, err := url.Parse("socks5://" + newTestSOCKS5Relay(t))
	require.NoError(t, err)

	pool := &scriptedPool{results: []scriptedPoolResult{{
		response: &ProxyResponse{ProxyURL: proxyURL},
	}}}
	client, err := New(
		clientTestCountry,
		pool,
		WithDirectTransport(clientTestDirectTransportFactory(t, echo.URL)),
		WithIPEchoEndpoints([]string{echo.URL + "/one", echo.URL + "/two"}),
	)
	require.NoError(t, err)

	err = client.PreflightSanityCheck(context.Background())
	require.ErrorIs(t, err, ErrPreflightLeaked)
	assert.True(t, IsPreflightLeak(err))
}

func TestClient_FetchProxiedIPRejectsAnEmptyPoolResponse(t *testing.T) {
	t.Parallel()

	client, err := New(
		clientTestCountry,
		&scriptedPool{results: []scriptedPoolResult{{response: nil}}},
	)
	require.NoError(t, err)

	_, err = client.fetchProxiedIP(context.Background())
	require.ErrorIs(t, err, ErrPreflightFailed)
}

func TestClient_PreflightWithRetryStopsAtLeakedEgress(t *testing.T) {
	t.Parallel()

	echo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, err := w.Write([]byte("198.51.100.10\n"))
		require.NoError(t, err)
	}))
	t.Cleanup(echo.Close)

	proxyURL, err := url.Parse("socks5://" + newTestSOCKS5Relay(t))
	require.NoError(t, err)

	pool := &scriptedPool{results: []scriptedPoolResult{{
		response: &ProxyResponse{ProxyURL: proxyURL},
	}}}
	client, err := New(
		clientTestCountry,
		pool,
		WithDirectTransport(clientTestDirectTransportFactory(t, echo.URL)),
		WithIPEchoEndpoints([]string{echo.URL + "/one", echo.URL + "/two"}),
	)
	require.NoError(t, err)

	err = client.PreflightSanityCheckWithRetry(context.Background())
	require.ErrorIs(t, err, ErrPreflightLeaked)
	assert.Len(t, pool.recordedRequests(), 1)
}

func TestClient_PreflightWithRetryRecoversFromPoolStartup(t *testing.T) {
	t.Parallel()

	directEcho := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, err := w.Write([]byte("198.51.100.10\n"))
		require.NoError(t, err)
	}))
	t.Cleanup(directEcho.Close)

	proxyEcho := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, err := w.Write([]byte("203.0.113.20\n"))
		require.NoError(t, err)
	}))
	t.Cleanup(proxyEcho.Close)

	proxyURL, err := url.Parse("socks5://" + newTestSOCKS5Relay(t))
	require.NoError(t, err)

	pool := &scriptedPool{results: []scriptedPoolResult{
		{err: ErrEgressUnavailable},
		{response: &ProxyResponse{ProxyURL: proxyURL}},
	}}
	client, err := New(
		clientTestCountry,
		pool,
		WithDirectTransport(clientTestDirectTransportFactory(t, directEcho.URL)),
		WithIPEchoEndpoints([]string{proxyEcho.URL + "/one", proxyEcho.URL + "/two"}),
	)
	require.NoError(t, err)

	require.NoError(t, client.PreflightSanityCheckWithRetry(context.Background()))
	assert.Len(t, pool.recordedRequests(), 2)
}

func TestClient_DefaultTransportRoutesThroughSOCKS5(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, err := w.Write([]byte("tunneled"))
		require.NoError(t, err)
	}))
	t.Cleanup(server.Close)

	proxyURL, err := url.Parse("socks5://" + newTestSOCKS5Relay(t))
	require.NoError(t, err)

	client, err := New(
		clientTestCountry,
		&StubPool{ProxyURL: proxyURL},
		WithRetryPolicy(clientTestRetryPolicy(1)),
	)
	require.NoError(t, err)

	response, err := client.Get(context.Background(), server.URL)
	require.NoError(t, err)
	closeClientTestResponse(t, response)

	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	assert.Equal(t, "tunneled", string(body))
}

func TestClient_FetchDirectIPReportsEndpointFailures(t *testing.T) {
	t.Parallel()

	echo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(echo.Close)

	client, err := New(
		clientTestCountry,
		&scriptedPool{},
		WithDirectTransport(clientTestDirectTransportFactory(t, echo.URL)),
		WithIPEchoEndpoints([]string{echo.URL + "/one", echo.URL + "/two"}),
	)
	require.NoError(t, err)

	_, err = client.fetchDirectIP(context.Background())
	require.ErrorIs(t, err, ErrPreflightFailed)
}

func TestClient_PreflightStopsAtTheFailedBoundary(t *testing.T) {
	t.Parallel()

	t.Run("direct IP failure skips proxy acquisition", func(t *testing.T) {
		t.Parallel()

		echo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
		}))
		t.Cleanup(echo.Close)

		pool := &scriptedPool{}
		client, err := New(
			clientTestCountry,
			pool,
			WithDirectTransport(clientTestDirectTransportFactory(t, echo.URL)),
			WithIPEchoEndpoints([]string{echo.URL + "/one", echo.URL + "/two"}),
		)
		require.NoError(t, err)

		err = client.PreflightSanityCheck(context.Background())
		require.ErrorIs(t, err, ErrPreflightFailed)
		assert.Empty(t, pool.recordedRequests())
	})

	t.Run("pool failure stops before proxy egress", func(t *testing.T) {
		t.Parallel()

		echo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, writeErr := w.Write([]byte("198.51.100.10\n"))
			require.NoError(t, writeErr)
		}))
		t.Cleanup(echo.Close)

		client, err := New(
			clientTestCountry,
			&scriptedPool{results: []scriptedPoolResult{{err: context.Canceled}}},
			WithDirectTransport(clientTestDirectTransportFactory(t, echo.URL)),
			WithIPEchoEndpoints([]string{echo.URL + "/one", echo.URL + "/two"}),
		)
		require.NoError(t, err)

		err = client.PreflightSanityCheckWithRetry(context.Background())
		require.ErrorIs(t, err, context.Canceled)
	})
}

func TestStubPool_RecordsCallsAndReturnsFailures(t *testing.T) {
	t.Parallel()

	proxyURL, err := url.Parse("socks5://proxy.test:1080")
	require.NoError(t, err)

	pool := &StubPool{
		ProxyURL: proxyURL,
		Pool:     "western",
		Exit:     clientTestCountry,
	}
	request := ProxyRequest{Country: clientTestCountry, FallbackOK: true}
	response, err := pool.ProxyForCountry(context.Background(), request)
	require.NoError(t, err)
	assert.Equal(t, proxyURL, response.ProxyURL)
	assert.Equal(t, "western", response.Pool)
	assert.Equal(t, clientTestCountry, response.ExitCountry)
	assert.Equal(t, 1, pool.Calls)
	assert.Equal(t, request, pool.LastRequest)

	pool.Fail = errors.New("pool unavailable")
	response, err = pool.ProxyForCountry(context.Background(), request)
	require.EqualError(t, err, "pool unavailable")
	assert.Nil(t, response)
	assert.Equal(t, 2, pool.Calls)
}

func newTestSOCKS5Relay(t *testing.T) string {
	t.Helper()

	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	require.NoError(t, err)

	done := make(chan struct{})
	go func() {
		defer close(done)

		for {
			connection, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}

			go relayTestSOCKS5Connection(connection)
		}
	}()

	t.Cleanup(func() {
		require.NoError(t, listener.Close())
		require.Eventually(t, func() bool {
			select {
			case <-done:
				return true
			default:
				return false
			}
		}, time.Second, 10*time.Millisecond)
	})

	return listener.Addr().String()
}

func relayTestSOCKS5Connection(connection net.Conn) {
	defer func() { _ = connection.Close() }()

	versionAndMethods := make([]byte, 2)
	if _, err := io.ReadFull(connection, versionAndMethods); err != nil || versionAndMethods[0] != 5 {
		return
	}

	methods := make([]byte, int(versionAndMethods[1]))
	if _, err := io.ReadFull(connection, methods); err != nil {
		return
	}

	if _, err := connection.Write([]byte{5, 0}); err != nil {
		return
	}

	requestHeader := make([]byte, 4)
	if _, err := io.ReadFull(connection, requestHeader); err != nil || requestHeader[0] != 5 || requestHeader[1] != 1 {
		return
	}

	host, ok := readTestSOCKS5Host(connection, requestHeader[3])
	if !ok {
		return
	}

	portBytes := make([]byte, 2)
	if _, err := io.ReadFull(connection, portBytes); err != nil {
		return
	}

	upstream, err := net.Dial(
		"tcp4",
		net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(portBytes)))),
	)
	if err != nil {
		_, _ = connection.Write([]byte{5, 1, 0, 1, 0, 0, 0, 0, 0, 0})

		return
	}
	defer func() { _ = upstream.Close() }()

	if _, err := connection.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0}); err != nil {
		return
	}

	go func() { _, _ = io.Copy(upstream, connection) }()
	_, _ = io.Copy(connection, upstream)
}

func readTestSOCKS5Host(connection net.Conn, addressType byte) (string, bool) {
	switch addressType {
	case 1:
		address := make([]byte, net.IPv4len)
		if _, err := io.ReadFull(connection, address); err != nil {
			return "", false
		}

		return net.IP(address).String(), true
	case 3:
		length := []byte{0}
		if _, err := io.ReadFull(connection, length); err != nil {
			return "", false
		}

		address := make([]byte, int(length[0]))
		if _, err := io.ReadFull(connection, address); err != nil {
			return "", false
		}

		return string(address), true
	case 4:
		address := make([]byte, net.IPv6len)
		if _, err := io.ReadFull(connection, address); err != nil {
			return "", false
		}

		return net.IP(address).String(), true
	default:
		return "", false
	}
}
