package pr0xteus

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxscope"
	thingsocks5 "github.com/things-go/go-socks5"
)

const proxyRelayDialTimeout = 15 * time.Second

// ProxyGateway exposes lease-authenticated SOCKS5 CONNECT on the controller.
// It has no egress network route: every destination connection is delegated to
// the selected cell's private SOCKS5 listener.
type ProxyGateway struct {
	manager    *Manager
	listenAddr string

	mu       sync.Mutex
	listener net.Listener
}

func NewProxyGateway(manager *Manager, listenAddr string) *ProxyGateway {
	return &ProxyGateway{manager: manager, listenAddr: listenAddr}
}

func (g *ProxyGateway) Start(ctx context.Context, errorsOut chan<- error) error {
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", g.listenAddr)
	if err != nil {
		return ctxerrors.Wrap(err, "listen for controller SOCKS5 gateway")
	}

	g.mu.Lock()
	g.listener = listener
	g.mu.Unlock()

	server := thingsocks5.NewServer(
		thingsocks5.WithCredential(proxyLeaseCredentials{manager: g.manager}),
		thingsocks5.WithResolver(cellResolver{}),
		thingsocks5.WithRule(&thingsocks5.PermitCommand{EnableConnect: true}),
		thingsocks5.WithDialAndRequest(g.dial),
		thingsocks5.WithLogger(newProxyGatewaySocksLogger(ctx)),
	)

	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, net.ErrClosed) {
			errorsOut <- ctxerrors.Wrap(err, "serve controller SOCKS5 gateway")
		}
	}()

	go func() {
		<-ctx.Done()

		if err := g.Close(); err != nil {
			ctxscope.GetLogger(ctx).Warn("close controller SOCKS5 gateway", "err", err)
		}
	}()

	return nil
}

func (g *ProxyGateway) Close() error {
	g.mu.Lock()
	listener := g.listener
	g.listener = nil
	g.mu.Unlock()

	if listener == nil {
		return nil
	}

	if err := listener.Close(); err != nil {
		return ctxerrors.Wrap(err, "close controller SOCKS5 gateway")
	}

	return nil
}

func (g *ProxyGateway) dial(
	ctx context.Context,
	network string,
	address string,
	request *thingsocks5.Request,
) (net.Conn, error) {
	if request.AuthContext == nil {
		return nil, ctxerrors.New("missing proxy lease authentication")
	}

	username := request.AuthContext.Payload["username"]
	password := request.AuthContext.Payload["password"]

	return g.manager.dialLease(ctx, username, password, network, address)
}

type proxyLeaseCredentials struct {
	manager *Manager
}

func (c proxyLeaseCredentials) Valid(username, password, _ string) bool {
	_, ok := c.manager.leases.Lookup(username, password)

	return ok
}

// cellResolver preserves client FQDNs for the upstream cell. go-socks5 writes
// the resolver result into the raw destination, so a nil IP retains the FQDN
// for the cell's WireGuard-provided DNS.
type cellResolver struct{}

func (cellResolver) Resolve(ctx context.Context, _ string) (context.Context, net.IP, error) {
	return ctx, nil, nil
}

type proxyGatewaySocksLogger struct {
	log func(socks5FailureReason)
}

func (l proxyGatewaySocksLogger) Errorf(format string, args ...any) {
	l.log(classifySOCKS5Failure(format, args...))
}

func newProxyGatewaySocksLogger(ctx context.Context) proxyGatewaySocksLogger {
	return proxyGatewaySocksLogger{log: func(reason socks5FailureReason) {
		logger := ctxscope.GetLogger(ctx)
		if reason == socks5FailureClientDisconnected {
			logger.Debug("controller SOCKS5 client disconnected", "reason", reason)

			return
		}

		logger.Warn("controller SOCKS5 relay failed", "reason", reason)
	}}
}

const (
	socks5FailureAuthenticationFailed socks5FailureReason = "authentication_failed"
	socks5FailureClientDisconnected   socks5FailureReason = "client_disconnected"
	socks5FailureProtocolError        socks5FailureReason = "protocol_error"
	socks5FailureUpstreamConnect      socks5FailureReason = "upstream_connect_failed"
)

type socks5FailureReason string

func classifySOCKS5Failure(format string, args ...any) socks5FailureReason {
	message := fmt.Sprintf(format, args...)

	switch {
	case strings.Contains(message, "EOF"):
		return socks5FailureClientDisconnected
	case strings.Contains(message, "failed to authenticate"):
		return socks5FailureAuthenticationFailed
	case strings.Contains(message, "connect to"):
		return socks5FailureUpstreamConnect
	default:
		return socks5FailureProtocolError
	}
}

type releaseConn struct {
	net.Conn
	once    sync.Once
	release func()
}

func (c *releaseConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(c.release)

	if err != nil {
		return ctxerrors.Wrap(err, "close cell proxy connection")
	}

	return nil
}
