package client

import (
	"errors"
	"net/http"

	"github.com/psyb0t/ctxerrors"
)

// dispatch is the Mode-aware entry point for Do. ModeVPNOnly goes
// straight to the proxy retry loop. ModePublicFirst tries the
// direct IPv4 transport once; on a 429/403/503/transport-error
// the SAME request is retried through the proxy retry loop. The
// direct attempt's failure does NOT count against the proxy retry
// budget.
func (c *Client) dispatch(req *http.Request) (*http.Response, error) {
	if c.mode == ModePublicFirst {
		return c.dispatchPublicFirst(req)
	}

	return c.do(req)
}

// dispatchPublicFirst executes the public-IP-first flow.
//
// Direct attempt:
//   - One pass through the IPv4-only direct transport.
//   - On 2xx/3xx → return immediately, public IP is the win.
//   - On 4xx where the status is NOT a rate-limit / forbidden →
//     return as-is. The proxy can't help fix a 404 / 400.
//   - On 429 / 403 / 503 / transport error → fall through to the
//     proxy path.
//
// Proxy attempt:
//   - Same as ModeVPNOnly's normal Do path (retry policy applies).
func (c *Client) dispatchPublicFirst(
	req *http.Request,
) (*http.Response, error) {
	directTransport := c.directTransportFactory()
	defer directTransport.CloseIdleConnections()

	directClient := &http.Client{
		Transport: directTransport,
		Jar:       c.jar,
	}

	if err := rewindBody(req); err != nil {
		return nil, ctxerrors.Wrap(err, "rewind body for direct attempt")
	}

	resp, err := directClient.Do(req)
	if err == nil && !shouldFallbackOnStatus(resp) {
		return resp, nil
	}

	// Drain + close so connection can be reused / not leaked.
	if resp != nil {
		_ = resp.Body.Close()
	}

	// Rewind body again — direct attempt consumed it.
	if err := rewindBody(req); err != nil {
		return nil, ctxerrors.Wrap(err, "rewind body for proxy attempt")
	}

	return c.do(req)
}

// shouldFallbackOnStatus reports whether a response from the
// direct attempt warrants retrying through the proxy.
//
//   - 429 Too Many Requests (rate-limited from public IP)
//   - 403 Forbidden (datacenter / known-aggregator IP blocked)
//   - 503 Service Unavailable (soft block disguised as temp error)
//
// Other 4xx (400, 401, 404, 410, etc.) are passed through — a
// proxy won't fix a malformed request or a real not-found.
func shouldFallbackOnStatus(resp *http.Response) bool {
	if resp == nil {
		return false
	}

	switch resp.StatusCode {
	case http.StatusTooManyRequests,
		http.StatusForbidden,
		http.StatusServiceUnavailable:
		return true
	}

	return false
}

// IsProxyMiss reports whether the error chain represents the
// "direct succeeded with non-fallback failure" case where the
// public-first dispatch deliberately did NOT engage the proxy.
//
// Placeholder hook for callers that want to log public-first
// hit-rate; returns false today.
func IsProxyMiss(err error) bool {
	return errors.Is(err, errProxyMissPlaceholder)
}

// errProxyMissPlaceholder is unexported and never returned today.
//

var errProxyMissPlaceholder = errors.New("proxy not engaged")
