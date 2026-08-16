package client

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/psyb0t/aichteeteapee"
	"github.com/psyb0t/ctxerrors"
)

// TunnelPoolClient is a PoolService implementation for pr0xteus's private,
// versioned HTTP API. Consumers create one at boot and reuse it for the
// lifetime of the service; only traffic sent to the returned SOCKS5 URL uses
// the selected egress tunnel.
type TunnelPoolClient struct {
	baseURL     string
	poolID      string
	bearerToken string
	doer        *http.Client
	timeout     time.Duration
}

// TunnelPoolOption tunes a TunnelPoolClient at construction.
type TunnelPoolOption func(*TunnelPoolClient)

// WithPool overrides the default pool name passed to pr0xteus. Each
// consumer usually has a preferred pool. Empty pool lets pr0xteus
// fall back to its own default routing.
func WithPool(pool string) TunnelPoolOption {
	return func(t *TunnelPoolClient) { t.poolID = pool }
}

// WithBearerToken sets the private control-plane token. The client never logs
// the token or includes it in a URL.
func WithBearerToken(token string) TunnelPoolOption {
	return func(t *TunnelPoolClient) { t.bearerToken = token }
}

// WithHTTPClient injects a custom HTTP client.
func WithHTTPClient(c *http.Client) TunnelPoolOption {
	return func(t *TunnelPoolClient) { t.doer = c }
}

// NewTunnelPoolClient builds the shared client.
//
// baseURL is pr0xteus's control-API URL (e.g. "http://pr0xteus:8000").
// timeout caps each ProxyForCountry HTTP roundtrip.
func NewTunnelPoolClient(
	baseURL string, timeout time.Duration, opts ...TunnelPoolOption,
) *TunnelPoolClient {
	c := &TunnelPoolClient{
		baseURL: baseURL,
		timeout: timeout,
	}

	for _, opt := range opts {
		opt(c)
	}

	if c.doer == nil {
		c.doer = &http.Client{Timeout: timeout}
	}

	return c
}

// PoolName returns the configured pool ID (read-only; for logs).
func (c *TunnelPoolClient) PoolName() string { return c.poolID }

// ProxyForCountry satisfies PoolService and converts the versioned response
// into the strongly typed ProxyResponse used by callers.
func (c *TunnelPoolClient) ProxyForCountry(
	ctx context.Context, req ProxyRequest,
) (*ProxyResponse, error) {
	body, err := c.doProxyRequest(ctx, req)
	if err != nil {
		return nil, err
	}

	return parseTunnelPoolResponse(body)
}

// tunnelPoolWireResponse is the JSON shape pr0xteus emits.
// Internal-only — the public ProxyResponse lives in pool.go.
type tunnelPoolWireResponse struct {
	URL         string `json:"url"`
	Pool        string `json:"pool"`
	ExitCountry string `json:"exitCountry"`
	ExitIP      string `json:"exitIP,omitempty"` //nolint:tagliatelle // API contract preserves the IP initialism.
}

type tunnelPoolWireRequest struct {
	Country      string `json:"country,omitempty"`
	Pool         string `json:"pool,omitempty"`
	ExcludeProxy string `json:"excludeProxy,omitempty"`
	FallbackOK   bool   `json:"fallbackOk,omitempty"`
}

func (c *TunnelPoolClient) doProxyRequest(
	ctx context.Context, proxyRequest ProxyRequest,
) ([]byte, error) {
	if c.timeout > 0 {
		var cancel context.CancelFunc

		ctx, cancel = context.WithTimeout(ctx, c.timeout)
		defer cancel()
	}

	req, err := c.newProxyRequest(ctx, proxyRequest)
	if err != nil {
		return nil, err
	}

	return c.executeProxyRequest(req)
}

func (c *TunnelPoolClient) newProxyRequest(
	ctx context.Context, proxyRequest ProxyRequest,
) (*http.Request, error) {
	wireRequest := tunnelPoolWireRequest{
		Country:    proxyRequest.Country,
		Pool:       c.poolID,
		FallbackOK: proxyRequest.FallbackOK,
	}
	if c.poolID != "" {
		wireRequest.Country = ""
	}

	if proxyRequest.ExcludeProxy != nil {
		wireRequest.ExcludeProxy = proxyRequest.ExcludeProxy.String()
	}

	payload, err := json.Marshal(wireRequest)
	if err != nil {
		return nil, ctxerrors.Wrap(ErrEgressUnavailable, "marshal proxy request")
	}

	endpoint := c.baseURL + "/v1/proxies"

	req, err := http.NewRequestWithContext(
		ctx, http.MethodPost, endpoint, bytes.NewReader(payload),
	)
	if err != nil {
		return nil, ctxerrors.Wrap(
			ErrEgressUnavailable,
			"new request "+endpoint+": "+err.Error(),
		)
	}

	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")

	if c.bearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.bearerToken)
	}

	return req, nil
}

// executeProxyRequest reads the size-limited response and wraps failures with
// ErrEgressUnavailable so callers retain the standard sentinel chain.
func (c *TunnelPoolClient) executeProxyRequest(req *http.Request) ([]byte, error) {
	endpoint := req.URL.String()

	resp, err := c.doer.Do(req)
	if err != nil {
		return nil, ctxerrors.Wrap(
			ErrEgressUnavailable,
			"POST "+endpoint+": "+err.Error(),
		)
	}

	defer func() { _ = resp.Body.Close() }()

	const maxBody = 1 << 16

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, ctxerrors.Wrap(
			ErrEgressUnavailable,
			"read body "+endpoint+": "+err.Error(),
		)
	}

	const truncateBodyAt = 200

	statusOK := resp.StatusCode >= http.StatusOK &&
		resp.StatusCode < http.StatusMultipleChoices
	if !statusOK {
		sentinel := ErrEgressUnavailable
		if isPoolExhaustedResponse(resp.StatusCode, body) {
			sentinel = ErrPoolExhausted
		}

		return nil, ctxerrors.Wrapf(
			sentinel,
			"pr0xteus %s returned %d: %s",
			endpoint, resp.StatusCode,
			truncateBody(string(body), truncateBodyAt),
		)
	}

	return body, nil
}

// isPoolExhaustedResponse detects pr0xteus's pool-exhausted response so the
// caller can fail fast (see retry.go shouldRetry) instead of retrying
// through the full backoff staircase. The server's writeAcquireError
// (internal/pkg/services/pr0xteus/api.go) maps BOTH its ErrPoolExhausted and
// ErrPoolUnavailable sentinels to the identical 503 status and
// aichteeteapee.ErrorResponseServiceUnavailable envelope — the two are
// indistinguishable on the wire. This client only exposes ErrPoolExhausted
// (see errors.go), so any response matching that shape maps there; that
// matches the documented pool contract ("May return ErrPoolExhausted when
// every option has been tried", pool.go).
func isPoolExhaustedResponse(statusCode int, body []byte) bool {
	if statusCode != http.StatusServiceUnavailable {
		return false
	}

	var envelope aichteeteapee.ErrorResponse
	if err := json.Unmarshal(body, &envelope); err != nil {
		return false
	}

	return envelope.Code == aichteeteapee.ErrorCodeServiceUnavailable
}

// parseTunnelPoolResponse decodes the wire JSON + lifts URL + Pool
// + ExitCountry into ProxyResponse.
func parseTunnelPoolResponse(body []byte) (*ProxyResponse, error) {
	var raw tunnelPoolWireResponse
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, ctxerrors.Wrap(
			ErrEgressUnavailable,
			"decode proxy response: "+err.Error(),
		)
	}

	if raw.URL == "" {
		return nil, ctxerrors.Wrap(
			ErrEgressUnavailable,
			"pr0xteus returned empty proxy URL",
		)
	}

	proxyURL, err := url.Parse(raw.URL)
	if err != nil {
		return nil, ctxerrors.Wrap(
			ErrEgressUnavailable,
			"parse proxy URL "+raw.URL+": "+err.Error(),
		)
	}

	return &ProxyResponse{
		ProxyURL:    proxyURL,
		Pool:        raw.Pool,
		ExitCountry: raw.ExitCountry,
		ExitIP:      raw.ExitIP,
	}, nil
}

// truncateBody cuts a body string to n runes max for log safety.
func truncateBody(s string, n int) string {
	if len(s) <= n {
		return s
	}

	return s[:n] + "…"
}
