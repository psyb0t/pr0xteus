package pr0xteus

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxscope"
)

const (
	httpProxyAuthHeader       = "Proxy-Authorization"
	httpProxyAuthenticate     = "Proxy-Authenticate"
	httpProxyConnectionHeader = "Proxy-Connection"
	httpProxyBasicScheme      = "Basic"
	httpProxyRealm            = "pr0xteus"
	httpProxyConnectSuccess   = "HTTP/1.1 200 Connection Established\r\n\r\n"
)

// HTTPProxyGateway exposes a lease-authenticated HTTP forward proxy on the
// controller. It supports absolute-form HTTP requests and CONNECT tunnels,
// and sends every destination connection through the selected cell.
type HTTPProxyGateway struct {
	manager    *Manager
	listenAddr string

	mu       sync.Mutex
	server   *http.Server
	hijacked map[net.Conn]struct{}
}

// NewHTTPProxyGateway builds the controller's standard HTTP proxy listener.
func NewHTTPProxyGateway(manager *Manager, listenAddr string) *HTTPProxyGateway {
	return &HTTPProxyGateway{
		manager:    manager,
		listenAddr: listenAddr,
		hijacked:   make(map[net.Conn]struct{}),
	}
}

// Start begins serving the HTTP proxy and arranges cleanup when the service
// context is cancelled.
func (g *HTTPProxyGateway) Start(ctx context.Context, errorsOut chan<- error) error {
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", g.listenAddr)
	if err != nil {
		return ctxerrors.Wrap(err, "listen for controller HTTP proxy")
	}

	server := &http.Server{
		Handler:           http.HandlerFunc(g.handle),
		ReadHeaderTimeout: httpReadHeaderTimeout,
	}

	g.mu.Lock()
	g.server = server
	g.mu.Unlock()

	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errorsOut <- ctxerrors.Wrap(err, "serve controller HTTP proxy")
		}
	}()

	go func() {
		<-ctx.Done()

		shutdownCtx, cancel := context.WithTimeout(
			context.WithoutCancel(ctx),
			httpShutdownTimeout,
		)
		defer cancel()

		if err := g.Close(shutdownCtx); err != nil {
			ctxscope.GetLogger(ctx).Warn("close controller HTTP proxy", "err", err)
		}
	}()

	return nil
}

// Close stops accepting new proxy connections and closes any hijacked CONNECT
// tunnels so their cell reservations are released.
func (g *HTTPProxyGateway) Close(ctx context.Context) error {
	g.mu.Lock()
	server := g.server
	g.server = nil

	connections := make([]net.Conn, 0, len(g.hijacked))
	for connection := range g.hijacked {
		connections = append(connections, connection)
	}

	g.mu.Unlock()

	var closeErr error

	for _, connection := range connections {
		if err := connection.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			closeErr = errors.Join(
				closeErr,
				ctxerrors.Wrap(err, "close controller HTTP proxy tunnel"),
			)
		}
	}

	if server == nil {
		return closeErr
	}

	if err := server.Shutdown(ctx); err != nil {
		return errors.Join(
			closeErr,
			ctxerrors.Wrap(err, "shutdown controller HTTP proxy"),
		)
	}

	return closeErr
}

func (g *HTTPProxyGateway) handle(w http.ResponseWriter, r *http.Request) {
	username, password, ok := proxyBasicCredentials(r)
	if !ok || !g.manager.validLease(username, password) {
		writeHTTPProxyAuthenticationRequired(w)

		return
	}

	if r.Method == http.MethodConnect {
		g.handleConnect(w, r, username, password)

		return
	}

	g.handleForwardRequest(w, r, username, password)
}

func (g *HTTPProxyGateway) handleConnect(
	w http.ResponseWriter, r *http.Request, username, password string,
) {
	if r.Host == "" {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)

		return
	}

	upstream, err := g.manager.dialLease(r.Context(), username, password, "tcp", r.Host)
	if err != nil {
		ctxscope.GetLogger(r.Context()).Warn(
			"controller HTTP proxy CONNECT failed",
			"reason", httpProxyFailureUpstreamConnect,
		)
		http.Error(w, http.StatusText(http.StatusBadGateway), http.StatusBadGateway)

		return
	}

	hijacker, ok := w.(http.Hijacker)
	if !ok {
		closeHTTPProxyConnection(r.Context(), upstream, "after hijack rejection")

		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)

		return
	}

	client, bufferedClient, err := hijacker.Hijack()
	if err != nil {
		closeHTTPProxyConnection(r.Context(), upstream, "after hijack failure")

		ctxscope.GetLogger(r.Context()).Warn(
			"controller HTTP proxy CONNECT failed",
			"reason", httpProxyFailureHijack,
		)

		return
	}

	g.trackHijacked(client)
	defer g.closeConnectTunnels(r.Context(), client, upstream)

	if _, err := io.WriteString(client, httpProxyConnectSuccess); err != nil {
		ctxscope.GetLogger(r.Context()).Debug(
			"controller HTTP proxy CONNECT response failed",
			"reason", httpProxyFailureRelay,
		)

		return
	}

	g.relayConnect(r.Context(), client, bufferedClient.Reader, upstream)
}

func (g *HTTPProxyGateway) handleForwardRequest(
	w http.ResponseWriter, r *http.Request, username, password string,
) {
	if !r.URL.IsAbs() || r.URL.Scheme != proxySchemeHTTP || r.URL.Host == "" {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)

		return
	}

	transport := &http.Transport{
		DisableKeepAlives: true,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			return g.manager.dialLease(ctx, username, password, network, address)
		},
	}
	defer transport.CloseIdleConnections()

	outbound := r.Clone(r.Context())
	outbound.RequestURI = ""
	outbound.Header = r.Header.Clone()
	stripProxyRequestHeaders(outbound.Header)

	response, err := transport.RoundTrip(outbound)
	if err != nil {
		ctxscope.GetLogger(r.Context()).Warn(
			"controller HTTP proxy request failed",
			"reason", httpProxyFailureUpstreamRequest,
		)
		http.Error(w, http.StatusText(http.StatusBadGateway), http.StatusBadGateway)

		return
	}

	defer func(ctx context.Context) {
		if closeErr := response.Body.Close(); closeErr != nil {
			ctxscope.GetLogger(ctx).Debug(
				"close HTTP proxy response body",
				"reason", httpProxyFailureRelay,
			)
		}
	}(r.Context())

	stripProxyResponseHeaders(response.Header)
	copyHTTPHeaders(w.Header(), response.Header)
	w.WriteHeader(response.StatusCode)

	if _, err := io.Copy(w, response.Body); err != nil {
		ctxscope.GetLogger(r.Context()).Debug(
			"controller HTTP proxy response relay failed",
			"reason", httpProxyFailureRelay,
		)
	}
}

func (g *HTTPProxyGateway) relayConnect(
	ctx context.Context, client net.Conn, clientReader io.Reader, upstream net.Conn,
) {
	errorsOut := make(chan error, httpProxyRelayStreams)
	go copyHTTPProxyStream(errorsOut, upstream, clientReader)
	go copyHTTPProxyStream(errorsOut, client, upstream)

	if err := <-errorsOut; err != nil && !errors.Is(err, net.ErrClosed) {
		ctxscope.GetLogger(ctx).Debug(
			"controller HTTP proxy CONNECT relay failed",
			"reason", httpProxyFailureRelay,
		)
	}

	if closeErr := client.Close(); closeErr != nil && !errors.Is(closeErr, net.ErrClosed) {
		ctxscope.GetLogger(ctx).Debug(
			"close HTTP proxy client after relay",
			"reason", httpProxyFailureRelay,
		)
	}

	if closeErr := upstream.Close(); closeErr != nil && !errors.Is(closeErr, net.ErrClosed) {
		ctxscope.GetLogger(ctx).Debug(
			"close HTTP proxy upstream after relay",
			"reason", httpProxyFailureRelay,
		)
	}

	if err := <-errorsOut; err != nil && !errors.Is(err, net.ErrClosed) {
		ctxscope.GetLogger(ctx).Debug(
			"controller HTTP proxy CONNECT relay closed",
			"reason", httpProxyFailureRelay,
		)
	}
}

func (g *HTTPProxyGateway) closeConnectTunnels(ctx context.Context, client, upstream net.Conn) {
	g.untrackHijacked(client)
	closeHTTPProxyConnection(ctx, client, "client tunnel")
	closeHTTPProxyConnection(ctx, upstream, "upstream tunnel")
}

func closeHTTPProxyConnection(ctx context.Context, connection net.Conn, phase string) {
	if err := connection.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		ctxscope.GetLogger(ctx).Debug(
			"close HTTP proxy connection",
			"phase", phase,
			"reason", httpProxyFailureRelay,
		)
	}
}

func copyHTTPProxyStream(errorsOut chan<- error, destination io.Writer, source io.Reader) {
	_, err := io.Copy(destination, source)
	errorsOut <- err
}

func (g *HTTPProxyGateway) trackHijacked(connection net.Conn) {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.hijacked[connection] = struct{}{}
}

func (g *HTTPProxyGateway) untrackHijacked(connection net.Conn) {
	g.mu.Lock()
	defer g.mu.Unlock()

	delete(g.hijacked, connection)
}

func proxyBasicCredentials(r *http.Request) (string, string, bool) {
	scheme, encoded, ok := strings.Cut(r.Header.Get(httpProxyAuthHeader), " ")
	if !ok || !strings.EqualFold(scheme, httpProxyBasicScheme) || encoded == "" {
		return "", "", false
	}

	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", "", false
	}

	username, password, ok := strings.Cut(string(decoded), ":")
	if !ok || username == "" || password == "" {
		return "", "", false
	}

	return username, password, true
}

func writeHTTPProxyAuthenticationRequired(w http.ResponseWriter) {
	w.Header().Set(httpProxyAuthenticate, httpProxyBasicScheme+` realm="`+httpProxyRealm+`"`)
	http.Error(w, http.StatusText(http.StatusProxyAuthRequired), http.StatusProxyAuthRequired)
}

func stripProxyRequestHeaders(header http.Header) {
	stripConnectionNominatedHeaders(header)

	for _, name := range []string{
		"Connection",
		"Keep-Alive",
		"Proxy-Connection",
		httpProxyAuthHeader,
		"TE",
		"Trailer",
		"Transfer-Encoding",
		"Upgrade",
	} {
		deleteHTTPHeader(header, name)
	}
}

func stripProxyResponseHeaders(header http.Header) {
	stripConnectionNominatedHeaders(header)

	for _, name := range []string{
		"Connection",
		"Keep-Alive",
		"Proxy-Connection",
		"TE",
		"Trailer",
		"Transfer-Encoding",
		"Upgrade",
	} {
		deleteHTTPHeader(header, name)
	}
}

func stripConnectionNominatedHeaders(header http.Header) {
	for name, values := range header {
		if !strings.EqualFold(name, "Connection") {
			continue
		}

		for _, value := range values {
			for target := range strings.SplitSeq(value, ",") {
				deleteHTTPHeader(header, strings.TrimSpace(target))
			}
		}
	}
}

func deleteHTTPHeader(header http.Header, target string) {
	for name := range header {
		if strings.EqualFold(name, target) {
			delete(header, name)
		}
	}
}

func copyHTTPHeaders(destination, source http.Header) {
	for name, values := range source {
		for _, value := range values {
			destination.Add(name, value)
		}
	}
}

type httpProxyFailureReason string

const (
	httpProxyFailureHijack          httpProxyFailureReason = "hijack_failed"
	httpProxyFailureRelay           httpProxyFailureReason = "relay_failed"
	httpProxyFailureUpstreamConnect httpProxyFailureReason = "upstream_connect_failed"
	httpProxyFailureUpstreamRequest httpProxyFailureReason = "upstream_request_failed"
)
