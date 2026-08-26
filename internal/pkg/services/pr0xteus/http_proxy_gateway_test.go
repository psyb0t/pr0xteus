package pr0xteus

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/psyb0t/pr0xteus/internal/pkg/cellproxy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHTTPProxyGateway_ProxiesHTTPAndHTTPSThroughLeasedCell(t *testing.T) {
	forwardedProxyAuth := make(chan string, 1)
	httpDestination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		forwardedProxyAuth <- r.Header.Get(httpProxyAuthHeader)
		w.Header().Set(httpProxyConnectionHeader, "keep-alive")
		_, _ = io.WriteString(w, "http proxy success")
	}))
	t.Cleanup(httpDestination.Close)

	httpsDestination := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "HTTPS proxy success")
	}))
	t.Cleanup(httpsDestination.Close)

	cellSocksAddr := freeTCPAddr(t)
	cellControlAddr := freeTCPAddr(t)
	cell := cellproxy.New(cellproxy.Config{
		CellID:          "cell-http-proxy-test",
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

	httpProxyAddr := freeTCPAddr(t)
	manager := gatewayTestManagerWithPublicAddresses(
		t,
		cellSocksAddr,
		"127.0.0.1:1080",
		httpProxyAddr,
	)
	proxyCtx, cancelProxy := context.WithCancel(context.Background())
	t.Cleanup(cancelProxy)
	gateway := NewHTTPProxyGateway(manager, httpProxyAddr)
	proxyErr := make(chan error, 1)
	require.NoError(t, gateway.Start(proxyCtx, proxyErr))
	t.Cleanup(func() { require.NoError(t, gateway.Close(context.Background())) })
	waitForTCPListener(t, httpProxyAddr)

	lease := issueGatewayTestLease(t, manager)
	proxyURL, err := url.Parse(lease.Proxies.HTTP)
	require.NoError(t, err)
	require.Equal(t, proxySchemeHTTP, proxyURL.Scheme)

	httpHeaders := assertHTTPProxyResponse(
		t,
		newHTTPProxyClient(proxyURL),
		httpDestination.URL,
		"http proxy success",
	)
	assert.Empty(t, httpHeaders.Get(httpProxyConnectionHeader))
	select {
	case proxyAuth := <-forwardedProxyAuth:
		assert.Empty(t, proxyAuth)
	case <-time.After(time.Second):
		t.Fatal("HTTP destination did not receive proxied request")
	}

	httpsTransport := httpsDestination.Client().Transport.(*http.Transport).Clone()
	httpsTransport.Proxy = http.ProxyURL(proxyURL)
	httpsTransport.DisableKeepAlives = true
	httpsClient := &http.Client{Transport: httpsTransport}
	t.Cleanup(httpsTransport.CloseIdleConnections)
	assertHTTPProxyResponse(t, httpsClient, httpsDestination.URL, "HTTPS proxy success")

	require.Eventually(t, func() bool {
		tunnel := manager.Pools()["western"].Snapshot()

		return tunnel != nil && tunnel.InFlight == 0
	}, time.Second, 10*time.Millisecond)
	assert.Empty(t, proxyErr)

	cancelCell()
	select {
	case err := <-cellErr:
		require.NoError(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("cell proxy did not stop")
	}
}

func TestHTTPProxyGateway_CONNECTForwardsBufferedClientData(t *testing.T) {
	destinationAddr, destinationDone := startBufferedConnectEchoServer(t)
	cellSocksAddr := freeTCPAddr(t)
	cellControlAddr := freeTCPAddr(t)
	cell := cellproxy.New(cellproxy.Config{
		CellID:          "cell-buffered-connect-test",
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

	httpProxyAddr := freeTCPAddr(t)
	manager := gatewayTestManagerWithPublicAddresses(
		t,
		cellSocksAddr,
		"127.0.0.1:1080",
		httpProxyAddr,
	)
	proxyCtx, cancelProxy := context.WithCancel(context.Background())
	t.Cleanup(cancelProxy)
	gateway := NewHTTPProxyGateway(manager, httpProxyAddr)
	proxyErr := make(chan error, 1)
	require.NoError(t, gateway.Start(proxyCtx, proxyErr))
	t.Cleanup(func() { require.NoError(t, gateway.Close(context.Background())) })
	waitForTCPListener(t, httpProxyAddr)

	lease := issueGatewayTestLease(t, manager)
	connection, err := net.Dial("tcp", httpProxyAddr)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, connection.Close()) })
	require.NoError(t, connection.SetDeadline(time.Now().Add(3*time.Second)))

	writeBufferedConnectRequest(t, connection, destinationAddr, lease)
	reader := bufio.NewReader(connection)
	readHTTPProxyConnectResponse(t, reader)
	payload := make([]byte, len(bufferedConnectPayload))
	_, err = io.ReadFull(reader, payload)
	require.NoError(t, err)
	assert.Equal(t, bufferedConnectPayload, string(payload))
	require.NoError(t, <-destinationDone)
	assert.Empty(t, proxyErr)

	cancelCell()
	select {
	case err := <-cellErr:
		require.NoError(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("cell proxy did not stop")
	}
}

func TestHTTPProxyGateway_RejectsUnknownLease(t *testing.T) {
	httpProxyAddr := freeTCPAddr(t)
	manager := gatewayTestManager(t, "127.0.0.1:1", "127.0.0.1:1080")
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	gateway := NewHTTPProxyGateway(manager, httpProxyAddr)
	errorsOut := make(chan error, 1)
	require.NoError(t, gateway.Start(ctx, errorsOut))
	t.Cleanup(func() { require.NoError(t, gateway.Close(context.Background())) })
	waitForTCPListener(t, httpProxyAddr)

	request, err := http.NewRequest(http.MethodGet, "http://"+httpProxyAddr+"/", nil)
	require.NoError(t, err)
	request.Header.Set(httpProxyAuthHeader, "Basic bm90LWEtbGVhc2U6d3Jvbmc=")
	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	assert.Equal(t, http.StatusProxyAuthRequired, response.StatusCode)
	assert.Equal(t, httpProxyBasicScheme+` realm="`+httpProxyRealm+`"`, response.Header.Get(httpProxyAuthenticate))
	assert.Empty(t, errorsOut)
}

func TestHTTPProxyGateway_LogsSafeUpstreamFailure(t *testing.T) {
	var output synchronizedBuffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })

	httpProxyAddr := freeTCPAddr(t)
	manager := gatewayTestManagerWithPublicAddresses(
		t,
		"127.0.0.1:1",
		"127.0.0.1:1080",
		httpProxyAddr,
	)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	gateway := NewHTTPProxyGateway(manager, httpProxyAddr)
	errorsOut := make(chan error, 1)
	require.NoError(t, gateway.Start(ctx, errorsOut))
	t.Cleanup(func() { require.NoError(t, gateway.Close(context.Background())) })
	waitForTCPListener(t, httpProxyAddr)

	lease := issueGatewayTestLease(t, manager)
	proxyURL, err := url.Parse(lease.Proxies.HTTP)
	require.NoError(t, err)
	response, err := newHTTPProxyClient(proxyURL).Get("http://secret-destination.example/")
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	assert.Equal(t, http.StatusBadGateway, response.StatusCode)

	require.Eventually(t, func() bool {
		return strings.Contains(output.String(), "controller HTTP proxy request failed")
	}, time.Second, 10*time.Millisecond)
	assert.Contains(t, output.String(), `"reason":"upstream_request_failed"`)
	assert.NotContains(t, output.String(), "secret-destination.example")
	assert.NotContains(t, output.String(), proxyURL.User.Username())
	assert.Empty(t, errorsOut)
}

func TestHTTPProxyGateway_RejectsInvalidListener(t *testing.T) {
	manager := gatewayTestManager(t, "127.0.0.1:1", "127.0.0.1:1080")
	gateway := NewHTTPProxyGateway(manager, "not-a-tcp-address")

	err := gateway.Start(context.Background(), make(chan error, 1))
	require.Error(t, err)
	assert.ErrorContains(t, err, "listen for controller HTTP proxy")
}

func TestHTTPProxyGateway_CloseWithoutStart(t *testing.T) {
	gateway := NewHTTPProxyGateway(
		gatewayTestManager(t, "127.0.0.1:1", "127.0.0.1:1080"),
		"127.0.0.1:8080",
	)

	require.NoError(t, gateway.Close(context.Background()))
}

func TestHTTPProxyGateway_ClosesListenerWhenContextCancels(t *testing.T) {
	listenAddr := freeTCPAddr(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	gateway := NewHTTPProxyGateway(
		gatewayTestManager(t, "127.0.0.1:1", "127.0.0.1:1080"),
		listenAddr,
	)
	errorsOut := make(chan error, 1)
	require.NoError(t, gateway.Start(ctx, errorsOut))
	waitForTCPListener(t, listenAddr)

	cancel()
	require.Eventually(t, func() bool {
		connection, err := net.DialTimeout("tcp", listenAddr, 20*time.Millisecond)
		if err != nil {
			return true
		}

		_ = connection.Close()

		return false
	}, time.Second, 10*time.Millisecond)
	assert.Empty(t, errorsOut)
}

func TestHTTPProxyGateway_RejectsIncompleteRequestsBeforeDial(t *testing.T) {
	manager := gatewayTestManager(t, "127.0.0.1:1", "127.0.0.1:1080")
	lease := issueGatewayTestLease(t, manager)
	proxyURL, err := url.Parse(lease.Proxies.HTTP)
	require.NoError(t, err)

	password, ok := proxyURL.User.Password()
	require.True(t, ok)
	authorization := httpProxyBasicScheme + " " + base64.StdEncoding.EncodeToString(
		[]byte(proxyURL.User.Username()+":"+password),
	)
	gateway := NewHTTPProxyGateway(manager, "127.0.0.1:8080")

	testCases := []struct {
		name    string
		request *http.Request
	}{
		{
			name:    "forward request without an absolute HTTP URL",
			request: httptest.NewRequest(http.MethodGet, "/relative", nil),
		},
		{
			name:    "CONNECT request without a target host",
			request: httptest.NewRequest(http.MethodConnect, "http://proxy.invalid", nil),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			tc.request.Host = ""
			tc.request.Header.Set(httpProxyAuthHeader, authorization)
			recorder := httptest.NewRecorder()

			gateway.handle(recorder, tc.request)

			assert.Equal(t, http.StatusBadRequest, recorder.Code)
		})
	}
}

func TestHTTPProxyGateway_RejectsCONNECTWhenResponseWriterCannotHijack(t *testing.T) {
	destination, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, destination.Close()) })
	go acceptAndCloseTCPConnection(destination)

	cellSocksAddr := freeTCPAddr(t)
	cellControlAddr := freeTCPAddr(t)
	cell := cellproxy.New(cellproxy.Config{
		CellID:          "cell-http-proxy-hijack-test",
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

	manager := gatewayTestManager(t, cellSocksAddr, "127.0.0.1:1080")
	lease := issueGatewayTestLease(t, manager)
	proxyURL, err := url.Parse(lease.Proxies.HTTP)
	require.NoError(t, err)
	password, ok := proxyURL.User.Password()
	require.True(t, ok)

	request := httptest.NewRequest(http.MethodConnect, "http://proxy.invalid", nil)
	request.Host = destination.Addr().String()
	request.Header.Set(
		httpProxyAuthHeader,
		httpProxyBasicScheme+" "+base64.StdEncoding.EncodeToString(
			[]byte(proxyURL.User.Username()+":"+password),
		),
	)
	recorder := httptest.NewRecorder()
	NewHTTPProxyGateway(manager, "127.0.0.1:8080").handle(recorder, request)
	assert.Equal(t, http.StatusInternalServerError, recorder.Code)

	cancelCell()
	select {
	case err := <-cellErr:
		require.NoError(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("cell proxy did not stop")
	}
}

func TestCopyHTTPProxyStream(t *testing.T) {
	t.Parallel()

	var destination bytes.Buffer
	errorsOut := make(chan error, 1)
	copyHTTPProxyStream(errorsOut, &destination, strings.NewReader("relay payload"))

	require.NoError(t, <-errorsOut)
	assert.Equal(t, "relay payload", destination.String())
}

func TestProxyBasicCredentials(t *testing.T) {
	t.Parallel()

	valid := base64.StdEncoding.EncodeToString([]byte("lease-id:lease-secret"))
	testCases := []struct {
		name     string
		header   string
		username string
		password string
		wantOK   bool
	}{
		{
			name:     "accepts basic credentials case insensitively",
			header:   "basic " + valid,
			username: "lease-id",
			password: "lease-secret",
			wantOK:   true,
		},
		{name: "rejects missing header"},
		{name: "rejects another scheme", header: "Bearer token"},
		{name: "rejects malformed encoding", header: "Basic %%%"},
		{
			name:   "rejects credentials without separator",
			header: "Basic " + base64.StdEncoding.EncodeToString([]byte("lease-id")),
		},
		{
			name:   "rejects an empty password",
			header: "Basic " + base64.StdEncoding.EncodeToString([]byte("lease-id:")),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			request := httptest.NewRequest(http.MethodGet, "http://proxy.invalid", nil)
			request.Header.Set(httpProxyAuthHeader, tc.header)
			username, password, ok := proxyBasicCredentials(request)

			assert.Equal(t, tc.wantOK, ok)
			assert.Equal(t, tc.username, username)
			assert.Equal(t, tc.password, password)
		})
	}
}

func TestHTTPProxyHeaderSanitization(t *testing.T) {
	t.Parallel()

	requestHeaders := http.Header{
		"Connection":              {"Keep-This-Out, X-Connection-Only"},
		"connection":              {"X-Lower-Connection-Only"},
		"Keep-Alive":              {"timeout=5"},
		httpProxyConnectionHeader: {"keep-alive"},
		httpProxyAuthHeader:       {"Basic secret"},
		"TE":                      {"trailers"},
		"Trailer":                 {"X-Trailer"},
		"Transfer-Encoding":       {"chunked"},
		"Upgrade":                 {"websocket"},
		"X-Connection-Only":       {"remove"},
		"X-Lower-Connection-Only": {"remove"},
		"X-Keep":                  {"keep"},
	}
	stripProxyRequestHeaders(requestHeaders)
	assert.Equal(t, http.Header{"X-Keep": {"keep"}}, requestHeaders)

	responseHeaders := http.Header{
		"Connection":              {"Keep-This-Out, X-Connection-Only"},
		"connection":              {"X-Lower-Connection-Only"},
		"Keep-Alive":              {"timeout=5"},
		httpProxyConnectionHeader: {"keep-alive"},
		"TE":                      {"trailers"},
		"Trailer":                 {"X-Trailer"},
		"Transfer-Encoding":       {"chunked"},
		"Upgrade":                 {"websocket"},
		"X-Connection-Only":       {"remove"},
		"X-Lower-Connection-Only": {"remove"},
		"X-Keep":                  {"keep-one", "keep-two"},
	}
	stripProxyResponseHeaders(responseHeaders)
	assert.Equal(t, http.Header{"X-Keep": {"keep-one", "keep-two"}}, responseHeaders)

	destination := http.Header{}
	copyHTTPHeaders(destination, responseHeaders)
	assert.Equal(t, responseHeaders.Values("X-Keep"), destination.Values("X-Keep"))
}

func TestHTTPProxyGateway_ClosesTrackedConnectClient(t *testing.T) {
	t.Parallel()

	client, server := net.Pipe()
	t.Cleanup(func() { require.NoError(t, server.Close()) })
	gateway := NewHTTPProxyGateway(
		gatewayTestManager(t, "127.0.0.1:1", "127.0.0.1:1080"),
		"127.0.0.1:8080",
	)
	gateway.trackHijacked(client)

	require.NoError(t, gateway.Close(context.Background()))
	_, err := client.Write([]byte("closed"))
	require.Error(t, err)
}

func assertHTTPProxyResponse(t *testing.T, client *http.Client, targetURL, want string) http.Header {
	t.Helper()

	response, err := client.Get(targetURL)
	require.NoError(t, err)
	defer func() { require.NoError(t, response.Body.Close()) }()

	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, response.StatusCode)
	assert.Equal(t, want, string(body))

	return response.Header
}

func newHTTPProxyClient(proxyURL *url.URL) *http.Client {
	transport := &http.Transport{
		DisableKeepAlives: true,
		Proxy:             http.ProxyURL(proxyURL),
	}

	return &http.Client{Transport: transport}
}

func acceptAndCloseTCPConnection(listener net.Listener) {
	connection, err := listener.Accept()
	if err != nil {
		return
	}

	_ = connection.Close()
}

const bufferedConnectPayload = "pipelined CONNECT payload"

func startBufferedConnectEchoServer(t *testing.T) (string, <-chan error) {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, listener.Close()) })

	done := make(chan error, 1)
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			done <- acceptErr

			return
		}
		defer connection.Close()

		payload := make([]byte, len(bufferedConnectPayload))
		if _, readErr := io.ReadFull(connection, payload); readErr != nil {
			done <- readErr

			return
		}

		_, writeErr := connection.Write(payload)
		done <- writeErr
	}()

	return listener.Addr().String(), done
}

func writeBufferedConnectRequest(
	t *testing.T, connection net.Conn, destinationAddr string, lease ProxyLease,
) {
	t.Helper()

	proxyURL, err := url.Parse(lease.Proxies.HTTP)
	require.NoError(t, err)
	password, ok := proxyURL.User.Password()
	require.True(t, ok)
	credentials := base64.StdEncoding.EncodeToString(
		[]byte(proxyURL.User.Username() + ":" + password),
	)
	request := strings.Join([]string{
		"CONNECT " + destinationAddr + " HTTP/1.1",
		"Host: " + destinationAddr,
		httpProxyAuthHeader + ": " + httpProxyBasicScheme + " " + credentials,
		"",
		bufferedConnectPayload,
	}, "\r\n")

	_, err = io.WriteString(connection, request)
	require.NoError(t, err)
}

func readHTTPProxyConnectResponse(t *testing.T, reader *bufio.Reader) {
	t.Helper()

	status, err := reader.ReadString('\n')
	require.NoError(t, err)
	assert.Equal(t, "HTTP/1.1 200 Connection Established\r\n", status)

	headers, err := reader.ReadString('\n')
	require.NoError(t, err)
	assert.Equal(t, "\r\n", headers)
}
