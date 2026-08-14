package client

import (
	"context"
	"net"
	"net/http"
	"time"
)

// IPv4-only dialer + transport timeouts for the direct path.
const (
	directDialTimeout           = 15 * time.Second
	directKeepAlive             = 30 * time.Second
	directTLSHandshakeTimeout   = 15 * time.Second
	directResponseHeaderTimeout = 30 * time.Second
	directMaxIdleConns          = 4
	directIdleConnTimeout       = 30 * time.Second
)

// buildDirectTransport returns an *http.Transport that dials the
// target directly (no proxy) using IPv4-only TCP. Used by:
//
//  1. ModePublicFirst's first attempt — the host's IP hits the
//     target directly because that IP buys preferential treatment.
//  2. PreflightSanityCheck's direct-IP probe — needs to know what
//     the world sees as the host's public IP WITHOUT going through
//     the proxy, so it can compare against the proxied result.
//
// IPv4-only because WireGuard tunnels are typically v4-only;
// allowing a v6 fallback dial would leak the host's IPv6 address
// to the target, defeating the tunnel.
func buildDirectTransport() *http.Transport {
	dialer := &net.Dialer{
		Timeout:   directDialTimeout,
		KeepAlive: directKeepAlive,
	}

	return &http.Transport{
		// IPv4-only dial. "tcp4" forces v4 lookup + v4 socket
		// family. No silent fallback to v6.
		DialContext: func(
			ctx context.Context, _, addr string,
		) (net.Conn, error) {
			return dialer.DialContext(ctx, "tcp4", addr)
		},

		// Reasonable handshake/response budgets so a dead direct
		// target surfaces quickly rather than blocking the
		// ModePublicFirst fallback decision.
		TLSHandshakeTimeout:   directTLSHandshakeTimeout,
		ResponseHeaderTimeout: directResponseHeaderTimeout,

		// Small connection pool — direct path doesn't expect
		// heavy reuse.
		MaxIdleConns:    directMaxIdleConns,
		IdleConnTimeout: directIdleConnTimeout,
	}
}
