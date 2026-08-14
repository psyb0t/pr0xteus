// Package client is the HTTP client library consumers use to send
// outbound requests through pr0xteus. It provides:
//
//   - ModeVPNOnly (default): every request routed through SOCKS5,
//     direct dials refused at the transport level. No leak path.
//   - ModePublicFirst: try the host's direct IPv4 first; on
//     429/403/503/transport-error fall back to SOCKS5 with full
//     retry policy.
//   - PreflightSanityCheck: at boot, query two IP-echo services
//     directly AND through the proxy, refuse to start if both
//     report the same address (= proxy not actually rerouting).
//   - Retry loop: 5-attempt staircase (same proxy → fresh TCP →
//     different proxy → fallback pool) with exponential backoff
//   - jitter.
//
// Construct one Client per (country, pool) tuple at boot; reuse
// it for the service's lifetime. Cheap to construct, expensive to
// throw away (the cookie jar is per-Client).
package client

import (
	"context"
	"net/http"
	"net/http/cookiejar"
	"net/url"

	"github.com/psyb0t/ctxerrors"
)

// Client is the proxy-routed HTTP client.
type Client struct {
	country   string
	pool      PoolService
	policy    RetryPolicy
	jar       http.CookieJar
	mode      Mode
	userAgent string

	// transportFactory builds per-attempt SOCKS5 transports.
	// Defaults to production buildTransport; tests inject via
	// WithTransport.
	transportFactory TransportFactory

	// directTransportFactory builds the IPv4-only direct transport
	// used in ModePublicFirst's first attempt and by
	// PreflightSanityCheck's direct-IP probe.
	directTransportFactory DirectTransportFactory

	// ipEchoEndpoints is the list of public-IP-echo URLs
	// preflight consults. Defaults to DefaultIPEchoEndpoints at
	// construction; tests inject deterministic httptest URLs.
	ipEchoEndpoints []string
}

// TransportFactory builds the per-attempt *http.Transport for one
// outbound request through the SOCKS5 proxy. The production
// factory wires the SOCKS5 dial path; tests inject a fake that
// bypasses the proxy for deterministic retry-loop assertions.
type TransportFactory func(proxyURL *url.URL, freshTCP bool) *http.Transport

// DirectTransportFactory builds the IPv4-only direct
// *http.Transport used by ModePublicFirst's first attempt and by
// PreflightSanityCheck's direct-IP probe.
type DirectTransportFactory func() *http.Transport

// Option configures a new Client.
type Option func(*Client)

// WithRetryPolicy overrides the default 5-attempt staircase.
func WithRetryPolicy(p RetryPolicy) Option {
	return func(c *Client) { c.policy = p }
}

// WithUserAgent sets the User-Agent header for outbound requests.
// Defaults to "" (no header set unless the caller's *http.Request
// already has one).
func WithUserAgent(ua string) Option {
	return func(c *Client) { c.userAgent = ua }
}

// WithTransport overrides the default SOCKS5 transport with a
// custom factory. Test-only seam — production code must not use
// it.
func WithTransport(f TransportFactory) Option {
	return func(c *Client) { c.transportFactory = f }
}

// WithDirectTransport overrides the default IPv4-only direct
// transport factory. Test-only seam.
func WithDirectTransport(f DirectTransportFactory) Option {
	return func(c *Client) { c.directTransportFactory = f }
}

// WithIPEchoEndpoints replaces the production IP-echo endpoint
// list used by PreflightSanityCheck. Test-only seam.
func WithIPEchoEndpoints(urls []string) Option {
	return func(c *Client) {
		c.ipEchoEndpoints = append([]string(nil), urls...)
	}
}

// New constructs a Client bound to country (ISO 3166-1 alpha-2).
// pool resolves country → proxy URL; nil pool or empty country is
// rejected.
func New(
	country string, pool PoolService, opts ...Option,
) (*Client, error) {
	if country == "" {
		return nil, ctxerrors.Wrap(
			ErrInvalidCountry, "country code is empty",
		)
	}

	if pool == nil {
		return nil, ctxerrors.Wrap(
			ErrEgressUnavailable, "pool service is nil",
		)
	}

	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "new cookie jar")
	}

	c := &Client{
		country: country,
		pool:    pool,
		policy:  DefaultRetryPolicy(),
		jar:     jar,
	}

	for _, opt := range opts {
		opt(c)
	}

	applyDefaults(c)

	return c, nil
}

// applyDefaults fills in the optional fields the caller didn't
// override via Option.
func applyDefaults(c *Client) {
	if c.transportFactory == nil {
		c.transportFactory = buildTransport
	}

	if c.directTransportFactory == nil {
		c.directTransportFactory = buildDirectTransport
	}

	if len(c.ipEchoEndpoints) == 0 {
		c.ipEchoEndpoints = append(
			[]string(nil), DefaultIPEchoEndpoints...,
		)
	}
}

// Mode returns the dispatch mode configured at construction.
func (c *Client) Mode() Mode { return c.mode }

// Country returns the country code bound at New().
func (c *Client) Country() string { return c.country }

// Do executes req through the egress pool with the retry policy
// applied.
//
// If req has a body, it MUST have GetBody set (http.NewRequest
// with bytes.NewReader / strings.NewReader / bytes.Buffer does
// this automatically). The retry loop calls GetBody to rewind
// between attempts; absent GetBody, the second attempt errors.
func (c *Client) Do(req *http.Request) (*http.Response, error) {
	if c.userAgent != "" && req.Header.Get(headerUserAgent) == "" {
		req.Header.Set(headerUserAgent, c.userAgent)
	}

	return c.dispatch(req)
}

// Get is a convenience wrapper for HTTP GET.
func (c *Client) Get(
	ctx context.Context, urlStr string,
) (*http.Response, error) {
	req, err := http.NewRequestWithContext(
		ctx, http.MethodGet, urlStr, nil,
	)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "build GET request")
	}

	return c.Do(req)
}

// do is the proxy-path retry loop. Per the design:
//
//	attempt 1 → original request
//	attempt 2 → same proxy, fresh TCP (no keep-alive reuse)
//	attempts 3-4 → ask pool for a DIFFERENT proxy (excludePrev)
//	attempt 5 → allow pool to escalate to fallback pool too
func (c *Client) do(req *http.Request) (*http.Response, error) {
	var (
		lastErr   error
		lastProxy *url.URL
	)

	for attempt := 1; attempt <= c.policy.MaxAttempts; attempt++ {
		if err := waitBackoff(
			req.Context(), c.policy, attempt,
		); err != nil {
			return nil, err
		}

		result := c.tryOnce(req, lastProxy, attempt)
		if result.halt != nil {
			return nil, result.halt
		}

		if result.resp != nil {
			return result.resp, nil
		}

		if result.proxy != nil {
			lastProxy = result.proxy
		}

		lastErr = result.retryErr
	}

	if lastErr == nil {
		lastErr = ErrNoAttemptError
	}

	return nil, ctxerrors.Wrap(
		errorsJoin(ErrEgressExhausted, lastErr),
		"all attempts failed",
	)
}

// attemptResult is the tri-state outcome of one retry iteration.
// Exactly one of resp / halt / retryErr is set on a normal return.
type attemptResult struct {
	// resp non-nil → success, return to caller immediately.
	resp *http.Response

	// halt non-nil → terminal error, return to caller without
	// further attempts (e.g. ctx cancelled, ErrPoolExhausted).
	halt error

	// retryErr non-nil → transient failure, loop should record
	// it and try again (subject to MaxAttempts).
	retryErr error

	// proxy is the proxy URL the attempt actually used. Carried
	// back so the next iteration can pass it as ExcludeProxy.
	proxy *url.URL
}

// tryOnce runs one attempt of the retry loop.
func (c *Client) tryOnce(
	req *http.Request, prevProxy *url.URL, attempt int,
) attemptResult {
	strategy := strategyFor(attempt)

	poolReq := ProxyRequest{
		Country:    c.country,
		FallbackOK: strategy.allowFallback && c.policy.UseFallbackPool,
	}
	if strategy.excludePrev && prevProxy != nil {
		poolReq.ExcludeProxy = prevProxy
	}

	poolResp, err := c.pool.ProxyForCountry(req.Context(), poolReq)
	if err != nil {
		wrapped := ctxerrors.Wrap(err, "pool.ProxyForCountry")
		if !shouldRetry(nil, err) {
			return attemptResult{halt: wrapped}
		}

		return attemptResult{retryErr: wrapped}
	}

	resp, err := c.fire(req, poolResp, strategy)
	if !shouldRetry(resp, err) {
		if err != nil {
			return attemptResult{
				halt: ctxerrors.Wrap(err, "egress http"),
			}
		}

		return attemptResult{resp: resp}
	}

	retryErr := err
	if retryErr == nil && resp != nil {
		_ = resp.Body.Close()
		retryErr = ctxerrors.Wrapf(
			ErrEgressUnavailable,
			"upstream http %d", resp.StatusCode,
		)
	}

	return attemptResult{
		proxy:    poolResp.ProxyURL,
		retryErr: retryErr,
	}
}

// fire runs a single HTTP request: build per-attempt transport,
// rewind body, fire request.
func (c *Client) fire(
	req *http.Request,
	pr *ProxyResponse,
	strategy attemptStrategy,
) (*http.Response, error) {
	transport := c.transportFactory(pr.ProxyURL, strategy.freshTCP)

	if err := rewindBody(req); err != nil {
		transport.CloseIdleConnections()

		return nil, ctxerrors.Wrap(err, "rewind body")
	}

	httpClient := &http.Client{
		Transport: transport,
		Jar:       c.jar,
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		transport.CloseIdleConnections()

		// Caller (or retry loop) classifies via shouldRetry; do
		// not double-wrap.
		return nil, err //nolint:wrapcheck
	}

	return resp, nil
}

const headerUserAgent = "User-Agent"
