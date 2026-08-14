package client

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/psyb0t/ctxerrors"
)

// DefaultIPEchoEndpoints is the production list of public-IP echo
// services PreflightSanityCheck consults. Two distinct providers
// so a single one being down / lying doesn't block deploy. Both
// return plain-text IP on the listed endpoints.
//
//nolint:gochecknoglobals // read-only slice constant
var DefaultIPEchoEndpoints = []string{
	"https://api.ipify.org",
	"https://ifconfig.me/ip",
}

// preflightFetchTimeout is the per-endpoint HTTP timeout used by
// the preflight check. Keep small — preflight blocks service
// startup, and a slow IP-echo provider shouldn't gate a deploy by
// more than a few seconds.
const preflightFetchTimeout = 30 * time.Second

// Preflight retry pacing. pr0xteus may need to spawn a fresh cell
// container (~30-90s of wg handshake + microsocks bind), and when
// multiple services boot simultaneously they all hit the same pool
// serially. Retrying lets every consumer pick up the cell the
// first arrival warmed.
const (
	preflightRetryBackoffStart = 5 * time.Second
	preflightRetryBackoffMax   = 30 * time.Second
	preflightRetryDeadline     = 10 * time.Minute
)

// PreflightSanityCheck verifies that the configured proxy actually
// changes the host's apparent public IP. Sequence:
//
//  1. Fetch the host's public IP via DIRECT (proxy-bypass) dial
//     against two IP-echo services in parallel; cross-check the
//     answers; pick the agreed value.
//  2. Ask pr0xteus for a SOCKS5 URL + dial the same IP-echo
//     services through the proxy; cross-check; pick the agreed
//     value.
//  3. Refuse to start if either step couldn't establish a value,
//     OR if both values are identical (= proxy not actually
//     rerouting → leak).
//
// Returns nil on success. ErrPreflightFailed / ErrPreflightLeaked
// otherwise.
//
// Callers MUST invoke this from service startup BEFORE production
// traffic. Use PreflightSanityCheckWithRetry to tolerate transient
// pool 503s (e.g. pr0xteus spawning a cold cell).
func (c *Client) PreflightSanityCheck(ctx context.Context) error {
	directIP, err := c.fetchDirectIP(ctx)
	if err != nil {
		return err
	}

	proxiedIP, err := c.fetchProxiedIP(ctx)
	if err != nil {
		return err
	}

	if directIP == proxiedIP {
		return ctxerrors.Wrapf(
			ErrPreflightLeaked,
			"preflight: direct (%s) == proxied (%s); proxy not routing",
			directIP, proxiedIP,
		)
	}

	return nil
}

// fetchDirectIP runs step 1 of preflight: query the IP-echo
// services through the IPv4-only direct transport.
func (c *Client) fetchDirectIP(ctx context.Context) (string, error) {
	ip, err := fetchAgreedPublicIP(
		ctx, &http.Client{
			Transport: c.directTransportFactory(),
			Timeout:   preflightFetchTimeout,
		},
		c.ipEchoEndpoints,
	)
	if err != nil {
		return "", ctxerrors.Wrap(err, "preflight: fetch direct public IP")
	}

	if ip == "" {
		return "", ctxerrors.Wrap(
			ErrPreflightFailed,
			"preflight: could not determine direct public IP",
		)
	}

	return ip, nil
}

// fetchProxiedIP runs step 2 of preflight: ask the pool for a
// SOCKS5 URL, then query the IP-echo services through it.
func (c *Client) fetchProxiedIP(ctx context.Context) (string, error) {
	poolResp, err := c.pool.ProxyForCountry(
		ctx, ProxyRequest{Country: c.country, FallbackOK: false},
	)
	if err != nil {
		return "", ctxerrors.Wrap(err, "preflight: pool.ProxyForCountry")
	}

	if poolResp == nil || poolResp.ProxyURL == nil {
		return "", ctxerrors.Wrap(
			ErrPreflightFailed,
			"preflight: pool returned empty proxy",
		)
	}

	proxyTransport := buildPreflightProxyTransport(poolResp.ProxyURL)
	defer proxyTransport.CloseIdleConnections()

	proxyClient := &http.Client{
		Transport: proxyTransport,
		Timeout:   preflightFetchTimeout,
	}

	ip, err := fetchAgreedPublicIP(ctx, proxyClient, c.ipEchoEndpoints)
	if err != nil {
		return "", ctxerrors.Wrap(err, "preflight: fetch proxied public IP")
	}

	if ip == "" {
		return "", ctxerrors.Wrap(
			ErrPreflightFailed,
			"preflight: could not determine proxied public IP",
		)
	}

	return ip, nil
}

// PreflightSanityCheckWithRetry wraps PreflightSanityCheck with
// exponential backoff. Retries on ErrEgressUnavailable (pool 503 —
// cell spawn in flight) + ErrPreflightFailed (transient IP-echo
// hiccup). Bails immediately on ErrPreflightLeaked because that's
// a security failure that won't fix itself.
func (c *Client) PreflightSanityCheckWithRetry(ctx context.Context) error {
	deadline, cancel := context.WithTimeout(ctx, preflightRetryDeadline)
	defer cancel()

	backoff := preflightRetryBackoffStart

	var lastErr error

	for attempt := 1; ; attempt++ {
		err := c.PreflightSanityCheck(deadline)
		if err == nil {
			return nil
		}

		// Security failure — don't retry, bail fast.
		if errors.Is(err, ErrPreflightLeaked) {
			return err
		}

		lastErr = err

		// Retryable: pool unavailable (503 / spawn in flight) or
		// flaky IP-echo. Anything else is a genuine failure.
		if !errors.Is(err, ErrEgressUnavailable) &&
			!errors.Is(err, ErrPreflightFailed) {
			return err
		}

		select {
		case <-deadline.Done():
			return ctxerrors.Wrapf(
				lastErr, "preflight gave up after %d attempt(s)", attempt,
			)
		case <-time.After(backoff):
		}

		backoff *= 2
		if backoff > preflightRetryBackoffMax {
			backoff = preflightRetryBackoffMax
		}
	}
}

// fetchAgreedPublicIP hits every endpoint in the supplied list in
// parallel via the given client. Returns the agreed-upon IP if at
// least two endpoints returned the SAME value; "" +
// ErrPreflightFailed if endpoints disagreed (one is lying or
// compromised).
func fetchAgreedPublicIP(
	ctx context.Context, doer *http.Client, endpoints []string,
) (string, error) {
	type result struct {
		ip  string
		err error
	}

	results := make(chan result, len(endpoints))

	var wg sync.WaitGroup

	for _, endpoint := range endpoints {
		wg.Add(1)

		go func(url string) {
			defer wg.Done()

			ip, err := fetchPublicIPFromEndpoint(ctx, doer, url)
			results <- result{ip: ip, err: err}
		}(endpoint)
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	counts := make(map[string]int)

	var lastErr error

	for r := range results {
		if r.err != nil {
			lastErr = r.err

			continue
		}

		counts[r.ip]++
	}

	const quorum = 2

	for ip, n := range counts {
		if n >= quorum {
			return ip, nil
		}
	}

	if lastErr != nil {
		return "", ctxerrors.Wrap(lastErr, "no quorum + endpoint errors")
	}

	return "", ctxerrors.Wrap(
		ErrPreflightFailed,
		"no quorum across IP-echo endpoints",
	)
}

// fetchPublicIPFromEndpoint hits one IP-echo URL and returns the
// public IP it reports. Handles both plain-text and JSON shapes.
func fetchPublicIPFromEndpoint(
	ctx context.Context, doer *http.Client, target string,
) (string, error) {
	req, err := http.NewRequestWithContext(
		ctx, http.MethodGet, target, nil,
	)
	if err != nil {
		return "", ctxerrors.Wrap(err, "build IP-echo request")
	}

	req.Header.Set("Accept", "text/plain, application/json")
	req.Header.Set("User-Agent", "pr0xteus-preflight/1.0")

	resp, err := doer.Do(req)
	if err != nil {
		return "", ctxerrors.Wrap(err, "GET "+target)
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < http.StatusOK ||
		resp.StatusCode >= http.StatusMultipleChoices {
		return "", ctxerrors.Wrapf(
			ErrPreflightFailed,
			"%s returned %d", target, resp.StatusCode,
		)
	}

	const maxBody = 1 << 12

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return "", ctxerrors.Wrap(err, "read body "+target)
	}

	return parseIPEchoBody(body), nil
}

// parseIPEchoBody extracts an IPv4 from either plain-text body or
// `{"ip":"1.2.3.4"}` JSON.
func parseIPEchoBody(body []byte) string {
	trimmed := strings.TrimSpace(string(body))

	if strings.HasPrefix(trimmed, "{") {
		var obj struct {
			IP string `json:"ip"`
		}
		if err := json.Unmarshal([]byte(trimmed), &obj); err == nil {
			return strings.TrimSpace(obj.IP)
		}
	}

	for line := range strings.SplitSeq(trimmed, "\n") {
		if v := strings.TrimSpace(line); v != "" {
			return v
		}
	}

	return ""
}

// IsPreflightLeak reports whether err contains ErrPreflightLeaked.
func IsPreflightLeak(err error) bool {
	return errors.Is(err, ErrPreflightLeaked)
}

// IsPreflightFailed reports whether err contains ErrPreflightFailed.
func IsPreflightFailed(err error) bool {
	return errors.Is(err, ErrPreflightFailed)
}

// buildPreflightProxyTransport returns an *http.Transport that
// routes via the supplied SOCKS5 proxy. HTTP/1.1-only, no keep-
// alive, one-shot dial — preflight fires twice then closes.
func buildPreflightProxyTransport(proxyURL *url.URL) *http.Transport {
	t := &http.Transport{
		TLSHandshakeTimeout: preflightFetchTimeout,
		ForceAttemptHTTP2:   false,
		MaxIdleConns:        2, //nolint:mnd // preflight is one-shot
		DisableKeepAlives:   true,
	}

	t.DialContext = func(
		ctx context.Context, _, addr string,
	) (net.Conn, error) {
		return dialSocks5(ctx, proxyURL, addr)
	}

	return t
}
