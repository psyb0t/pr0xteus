package pr0xteus

import (
	"context"
	"errors"
	"net"
	"sync"
	"time"

	thingsocks5 "github.com/things-go/go-socks5"
	"golang.org/x/net/proxy"
)

const proxyGatewayDialTimeout = 15 * time.Second

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
		return err
	}

	g.mu.Lock()
	g.listener = listener
	g.mu.Unlock()

	server := thingsocks5.NewServer(
		thingsocks5.WithCredential(proxyLeaseCredentials{manager: g.manager}),
		thingsocks5.WithResolver(cellResolver{}),
		thingsocks5.WithRule(&thingsocks5.PermitCommand{EnableConnect: true}),
		thingsocks5.WithDialAndRequest(g.dial),
	)

	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, net.ErrClosed) {
			errorsOut <- err
		}
	}()

	go func() {
		<-ctx.Done()
		_ = g.Close()
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

	return listener.Close()
}

func (g *ProxyGateway) dial(
	ctx context.Context,
	network string,
	address string,
	request *thingsocks5.Request,
) (net.Conn, error) {
	if request.AuthContext == nil {
		return nil, errors.New("missing proxy lease authentication")
	}

	username := request.AuthContext.Payload["username"]
	password := request.AuthContext.Payload["password"]
	acquisition, err := g.manager.AcquireForLease(username, password)
	if err != nil {
		return nil, err
	}

	dialContext, cancel := context.WithTimeout(ctx, proxyGatewayDialTimeout)
	defer cancel()

	upstream, err := proxy.SOCKS5(
		network,
		acquisition.Tunnel.GatewayAddr,
		nil,
		&net.Dialer{Timeout: proxyGatewayDialTimeout},
	)
	if err != nil {
		g.manager.Release(acquisition)

		return nil, err
	}

	contextDialer, ok := upstream.(proxy.ContextDialer)
	if !ok {
		g.manager.Release(acquisition)

		return nil, errors.New("cell SOCKS5 dialer lacks context support")
	}

	connection, err := contextDialer.DialContext(dialContext, network, address)
	if err != nil {
		g.manager.Release(acquisition)

		return nil, err
	}

	return &releaseConn{Conn: connection, release: func() { g.manager.Release(acquisition) }}, nil
}

type proxyLeaseCredentials struct {
	manager *Manager
}

func (c proxyLeaseCredentials) Valid(username, password, _ string) bool {
	_, ok := c.manager.leases.Lookup(username, password)

	return ok
}

// cellResolver deliberately does not resolve client destinations on the
// controller. go-socks5 requires a resolver before it forwards the raw FQDN to
// the upstream SOCKS5 cell, where WireGuard-provided DNS resolves it.
type cellResolver struct{}

func (cellResolver) Resolve(ctx context.Context, _ string) (context.Context, net.IP, error) {
	return ctx, net.IPv4zero, nil
}

type releaseConn struct {
	net.Conn
	once    sync.Once
	release func()
}

func (c *releaseConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(c.release)

	return err
}
