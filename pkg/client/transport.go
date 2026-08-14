package client

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/psyb0t/ctxerrors"
	xproxy "golang.org/x/net/proxy"
)

// Hardened-transport defaults.
const (
	transportTLSHandshakeTimeout   = 15 * time.Second
	transportResponseHeaderTimeout = 30 * time.Second
	transportExpectContinueTimeout = 1 * time.Second
	transportMaxIdleConns          = 10
	transportMaxIdleConnsPerHost   = 2
	transportIdleConnTimeout       = 60 * time.Second
)

// buildTransport returns an *http.Transport that routes every dial
// through the given SOCKS5 proxy. pr0xteus only emits SOCKS5 URLs,
// so this is the production path.
//
// freshTCP=true disables connection reuse on this Transport (used
// by retry attempt 2 to force a brand-new TCP).
func buildTransport(
	proxyURL *url.URL, freshTCP bool,
) *http.Transport {
	t := &http.Transport{
		// Proxy=nil intentional — the proxy is invoked via
		// DialContext below, where we route SOCKS5 manually.
		Proxy: nil,

		TLSHandshakeTimeout:   transportTLSHandshakeTimeout,
		ResponseHeaderTimeout: transportResponseHeaderTimeout,
		ExpectContinueTimeout: transportExpectContinueTimeout,

		MaxIdleConns:        transportMaxIdleConns,
		MaxIdleConnsPerHost: transportMaxIdleConnsPerHost,
		IdleConnTimeout:     transportIdleConnTimeout,

		// DisableKeepAlives when the retry policy asked for a
		// fresh TCP (attempt 2's "same proxy, no idle reuse").
		DisableKeepAlives: freshTCP,
	}

	// DialContext routes every connection through SOCKS5.
	t.DialContext = func(
		ctx context.Context, _, addr string,
	) (net.Conn, error) {
		return dialSocks5(ctx, proxyURL, addr)
	}

	return t
}

// dialSocks5 opens a TCP conn to targetAddr via the SOCKS5
// proxyURL. Uses x/net/proxy's Dialer which handles the SOCKS5
// greeting + CONNECT internally. The forward dialer is constrained
// to tcp4 so the same IPv4 leak guarantee applies as the direct
// path.
func dialSocks5(
	ctx context.Context, proxyURL *url.URL, targetAddr string,
) (net.Conn, error) {
	forward := &ipv4OnlyDialer{}

	socks, err := xproxy.SOCKS5("tcp", proxyURL.Host, nil, forward)
	if err != nil {
		return nil, ctxerrors.Wrap(
			ErrEgressUnavailable, "build socks5 dialer: "+err.Error(),
		)
	}

	contextDialer, ok := socks.(xproxy.ContextDialer)
	if !ok {
		return nil, ctxerrors.Wrap(
			ErrEgressUnavailable, "socks5 dialer missing ContextDialer",
		)
	}

	conn, err := contextDialer.DialContext(ctx, "tcp", targetAddr)
	if err != nil {
		return nil, ctxerrors.Wrap(
			ErrEgressUnavailable, "socks5 dial target: "+err.Error(),
		)
	}

	return conn, nil
}

// ipv4OnlyDialer is the forward dialer x/net/proxy's SOCKS5 client
// uses to reach the proxy itself. Constrained to tcp4 so v6
// addresses for the proxy don't surface in DNS-leak paths.
type ipv4OnlyDialer struct{}

func (ipv4OnlyDialer) Dial(_, address string) (net.Conn, error) {
	var d net.Dialer

	conn, err := d.Dial("tcp4", address)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "ipv4 dial")
	}

	return conn, nil
}
