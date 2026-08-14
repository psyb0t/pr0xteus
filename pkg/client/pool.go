package client

import (
	"context"
	"net/url"
)

// PoolService is the seam Client uses to resolve which SOCKS5
// proxy a request should go through. The production implementation
// is TunnelPoolClient (hits pr0xteus over HTTP). Tests use StubPool.
type PoolService interface {
	// ProxyForCountry returns a proxy URL for the given country's
	// pool, optionally excluding a previously-failed proxy and
	// optionally allowing fallback to a neighbor pool. May return
	// ErrPoolExhausted when every option has been tried.
	ProxyForCountry(
		ctx context.Context, req ProxyRequest,
	) (*ProxyResponse, error)
}

// ProxyRequest is the per-call shape Client sends to the pool.
// ExcludeProxy is set on retry attempts so the pool can pick a
// DIFFERENT tunnel than the one that just failed. FallbackOK is
// set on the last attempt so the pool can escalate to a neighbor
// pool when the primary is exhausted.
type ProxyRequest struct {
	// Country is the ISO 3166-1 alpha-2 code the Client was
	// constructed against. The pool maps this to a pool name via
	// its own routing config (config/egress-routing.yaml in
	// pr0xteus).
	Country string

	// ExcludeProxy, if non-nil, signals "don't return this proxy
	// again; it just failed for me". The pool's recently-failed
	// cache absorbs the exclusion and picks a different tunnel.
	ExcludeProxy *url.URL

	// FallbackOK lets the pool escalate to its configured fallback
	// pool when the primary is exhausted. Set to true on the last
	// retry attempt; false otherwise.
	FallbackOK bool
}

// ProxyResponse is the pool's answer. ProxyURL is the SOCKS5 URL
// Client dials through. Pool / ExitCountry / ExitIP are telemetry
// so logs + metrics show which exit the request went through.
type ProxyResponse struct {
	ProxyURL    *url.URL
	Pool        string // pool name (e.g. "western_eu")
	ExitCountry string // ISO 3166-1 alpha-2 of the exit server
	ExitIP      string // reserved optional metadata; normally empty
}

// ─── Stub for tests ───────────────────────────────────────────────

// StubPool is a deterministic PoolService for unit tests. Returns
// a fixed proxy URL by default; Fail forces ProxyForCountry to
// return that error instead.
type StubPool struct {
	ProxyURL *url.URL
	Pool     string
	Exit     string
	Fail     error

	// Calls records how many times ProxyForCountry was invoked,
	// useful for asserting retry behavior.
	Calls int

	// LastRequest records the last ProxyRequest received so tests
	// can assert that retries pass ExcludeProxy / FallbackOK
	// correctly.
	LastRequest ProxyRequest
}

// ProxyForCountry satisfies PoolService.
func (s *StubPool) ProxyForCountry(
	_ context.Context, req ProxyRequest,
) (*ProxyResponse, error) {
	s.Calls++
	s.LastRequest = req

	if s.Fail != nil {
		return nil, s.Fail
	}

	return &ProxyResponse{
		ProxyURL:    s.ProxyURL,
		Pool:        s.Pool,
		ExitCountry: s.Exit,
	}, nil
}
