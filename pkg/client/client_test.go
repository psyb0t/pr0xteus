package client

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	clientTestCountry      = "DE"
	clientTestProxyOneHost = "proxy-one.test:1080"
	clientTestProxyTwoHost = "proxy-two.test:1080"
	clientTestProxyTriHost = "proxy-three.test:1080"
	clientTestUserAgent    = "pr0xteus-client-test/1.0"
)

type scriptedPoolResult struct {
	response *ProxyResponse
	err      error
}

type scriptedPool struct {
	mu sync.Mutex

	results  []scriptedPoolResult
	requests []ProxyRequest
}

func (p *scriptedPool) ProxyForCountry(
	_ context.Context, request ProxyRequest,
) (*ProxyResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.requests = append(p.requests, request)
	resultIndex := len(p.requests) - 1
	if resultIndex >= len(p.results) {
		resultIndex = len(p.results) - 1
	}

	result := p.results[resultIndex]

	return result.response, result.err
}

func (p *scriptedPool) recordedRequests() []ProxyRequest {
	p.mu.Lock()
	defer p.mu.Unlock()

	return append([]ProxyRequest(nil), p.requests...)
}

func TestClient_NewValidatesAndAppliesDefaults(t *testing.T) {
	t.Parallel()

	pool := &scriptedPool{results: []scriptedPoolResult{{
		response: clientTestProxyResponse(t, clientTestProxyOneHost),
	}}}
	testCases := []struct {
		name    string
		country string
		pool    PoolService
		wantErr error
	}{
		{name: "empty country", pool: pool, wantErr: ErrInvalidCountry},
		{name: "nil pool", country: clientTestCountry, wantErr: ErrEgressUnavailable},
		{name: "valid", country: clientTestCountry, pool: pool},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			client, err := New(tc.country, tc.pool)
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
				assert.Nil(t, client)

				return
			}

			require.NoError(t, err)
			require.NotNil(t, client)
			assert.Equal(t, ModeVPNOnly, client.Mode())
			assert.Equal(t, clientTestCountry, client.Country())
			assert.NotNil(t, client.transportFactory)
			assert.NotNil(t, client.directTransportFactory)
			assert.Equal(t, DefaultIPEchoEndpoints, client.ipEchoEndpoints)
		})
	}
}

func TestClient_DoSendsUserAgentThroughSelectedProxy(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, clientTestUserAgent, r.Header.Get(headerUserAgent))
		assert.Equal(t, "/resource", r.URL.Path)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	pool := &scriptedPool{results: []scriptedPoolResult{{
		response: clientTestProxyResponse(t, clientTestProxyOneHost),
	}}}
	client, err := New(
		clientTestCountry,
		pool,
		WithUserAgent(clientTestUserAgent),
		WithRetryPolicy(clientTestRetryPolicy(1)),
		WithTransport(clientTestTransportFactory(t, server.URL, nil)),
	)
	require.NoError(t, err)

	response, err := client.Get(context.Background(), server.URL+"/resource")
	require.NoError(t, err)
	closeClientTestResponse(t, response)
	assert.Equal(t, http.StatusOK, response.StatusCode)

	requests := pool.recordedRequests()
	require.Len(t, requests, 1)
	assert.Equal(t, clientTestCountry, requests[0].Country)
	assert.Nil(t, requests[0].ExcludeProxy)
	assert.False(t, requests[0].FallbackOK)
}

func TestClient_RetriesUsingFiveAttemptRecoveryStaircase(t *testing.T) {
	t.Parallel()

	var upstreamCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if upstreamCalls.Add(1) < defaultMaxAttempts {
			w.WriteHeader(http.StatusServiceUnavailable)

			return
		}

		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	pool := &scriptedPool{results: []scriptedPoolResult{
		{response: clientTestProxyResponse(t, clientTestProxyOneHost)},
		{response: clientTestProxyResponse(t, clientTestProxyOneHost)},
		{response: clientTestProxyResponse(t, clientTestProxyTwoHost)},
		{response: clientTestProxyResponse(t, clientTestProxyTwoHost)},
		{response: clientTestProxyResponse(t, clientTestProxyTriHost)},
	}}
	var freshTCPAttempts []bool
	client, err := New(
		clientTestCountry,
		pool,
		WithRetryPolicy(clientTestRetryPolicy(defaultMaxAttempts)),
		WithTransport(clientTestTransportFactory(t, server.URL, &freshTCPAttempts)),
	)
	require.NoError(t, err)

	response, err := client.Get(context.Background(), server.URL)
	require.NoError(t, err)
	closeClientTestResponse(t, response)
	assert.Equal(t, http.StatusOK, response.StatusCode)
	assert.Equal(t, int32(defaultMaxAttempts), upstreamCalls.Load())
	assert.Equal(t, []bool{false, true, false, false, false}, freshTCPAttempts)

	requests := pool.recordedRequests()
	require.Len(t, requests, defaultMaxAttempts)
	assert.Nil(t, requests[0].ExcludeProxy)
	assert.Nil(t, requests[1].ExcludeProxy)
	require.NotNil(t, requests[2].ExcludeProxy)
	assert.Equal(t, clientTestProxyOneHost, requests[2].ExcludeProxy.Host)
	require.NotNil(t, requests[3].ExcludeProxy)
	assert.Equal(t, clientTestProxyTwoHost, requests[3].ExcludeProxy.Host)
	require.NotNil(t, requests[4].ExcludeProxy)
	assert.Equal(t, clientTestProxyTwoHost, requests[4].ExcludeProxy.Host)
	assert.True(t, requests[4].FallbackOK)
}

func TestClient_StopsImmediatelyForExhaustedPool(t *testing.T) {
	t.Parallel()

	pool := &scriptedPool{results: []scriptedPoolResult{{
		err: ErrPoolExhausted,
	}}}
	client, err := New(
		clientTestCountry,
		pool,
		WithRetryPolicy(clientTestRetryPolicy(defaultMaxAttempts)),
	)
	require.NoError(t, err)

	request := httptest.NewRequest(http.MethodGet, "http://upstream.test", nil)
	response, err := client.Do(request)
	require.ErrorIs(t, err, ErrPoolExhausted)
	assert.Nil(t, response)
	assert.Len(t, pool.recordedRequests(), 1)
}

func TestClient_ExhaustionPreservesTheEgressSentinel(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(server.Close)

	pool := &scriptedPool{results: []scriptedPoolResult{{
		response: clientTestProxyResponse(t, clientTestProxyOneHost),
	}}}
	client, err := New(
		clientTestCountry,
		pool,
		WithRetryPolicy(clientTestRetryPolicy(1)),
		WithTransport(clientTestTransportFactory(t, server.URL, nil)),
	)
	require.NoError(t, err)

	response, err := client.Get(context.Background(), server.URL)
	require.ErrorIs(t, err, ErrEgressExhausted)
	assert.Nil(t, response)
	assert.Len(t, pool.recordedRequests(), 1)
}

func TestClient_ReportsInvalidGetURLsAndProxyMissState(t *testing.T) {
	t.Parallel()

	client, err := New(
		clientTestCountry,
		&scriptedPool{},
	)
	require.NoError(t, err)

	response, err := client.Get(context.Background(), "://invalid-url")
	require.Error(t, err)
	assert.Nil(t, response)
	assert.False(t, IsProxyMiss(err))
	assert.True(t, errors.Is(
		errorsJoin(ErrEgressExhausted, ErrEgressUnavailable),
		ErrEgressUnavailable,
	))
}

func TestClient_FailsClosedWhenTheRequestCannotBeRetried(t *testing.T) {
	t.Parallel()

	pool := &scriptedPool{results: []scriptedPoolResult{{
		response: clientTestProxyResponse(t, clientTestProxyOneHost),
	}}}
	client, err := New(
		clientTestCountry,
		pool,
		WithRetryPolicy(clientTestRetryPolicy(1)),
		WithTransport(clientTestTransportFactory(t, "http://upstream.test", nil)),
	)
	require.NoError(t, err)

	request := httptest.NewRequest(
		http.MethodPost,
		"http://upstream.test",
		io.NopCloser(strings.NewReader("one-shot-body")),
	)
	response, err := client.Do(request)
	require.ErrorIs(t, err, ErrEgressExhausted)
	assert.Nil(t, response)
	assert.Len(t, pool.recordedRequests(), 1)
}

func TestClient_RetriesTransportFailuresWithoutLeakingTheDirectPath(t *testing.T) {
	t.Parallel()

	pool := &scriptedPool{results: []scriptedPoolResult{{
		response: clientTestProxyResponse(t, clientTestProxyOneHost),
	}}}
	client, err := New(
		clientTestCountry,
		pool,
		WithRetryPolicy(clientTestRetryPolicy(1)),
		WithTransport(func(*url.URL, bool) *http.Transport {
			return &http.Transport{
				DialContext: func(context.Context, string, string) (net.Conn, error) {
					return nil, errors.New("socks5 tunnel unavailable")
				},
			}
		}),
	)
	require.NoError(t, err)

	response, err := client.Get(context.Background(), "http://upstream.test")
	require.ErrorIs(t, err, ErrEgressExhausted)
	assert.Nil(t, response)
	assert.Len(t, pool.recordedRequests(), 1)
}

func TestClient_PublicFirstDispatchesAndRewindsBody(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	var bodies []string
	var bodiesMu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		bodiesMu.Lock()
		bodies = append(bodies, string(body))
		bodiesMu.Unlock()

		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)

			return
		}

		w.WriteHeader(http.StatusCreated)
	}))
	t.Cleanup(server.Close)

	pool := &scriptedPool{results: []scriptedPoolResult{{
		response: clientTestProxyResponse(t, clientTestProxyOneHost),
	}}}
	client, err := New(
		clientTestCountry,
		pool,
		WithMode(ModePublicFirst),
		WithRetryPolicy(clientTestRetryPolicy(1)),
		WithTransport(clientTestTransportFactory(t, server.URL, nil)),
		WithDirectTransport(clientTestDirectTransportFactory(t, server.URL)),
	)
	require.NoError(t, err)

	request, err := http.NewRequest(
		http.MethodPost,
		server.URL,
		strings.NewReader("retry-safe-body"),
	)
	require.NoError(t, err)
	response, err := client.Do(request)
	require.NoError(t, err)
	closeClientTestResponse(t, response)
	assert.Equal(t, http.StatusCreated, response.StatusCode)
	assert.Len(t, pool.recordedRequests(), 1)
	bodiesMu.Lock()
	assert.Equal(t, []string{"retry-safe-body", "retry-safe-body"}, bodies)
	bodiesMu.Unlock()
}

func TestClient_PublicFirstReturnsNonFallbackResponseWithoutProxy(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(server.Close)

	pool := &scriptedPool{results: []scriptedPoolResult{{
		response: clientTestProxyResponse(t, clientTestProxyOneHost),
	}}}
	client, err := New(
		clientTestCountry,
		pool,
		WithMode(ModePublicFirst),
		WithDirectTransport(clientTestDirectTransportFactory(t, server.URL)),
	)
	require.NoError(t, err)

	response, err := client.Get(context.Background(), server.URL)
	require.NoError(t, err)
	closeClientTestResponse(t, response)
	assert.Equal(t, http.StatusNotFound, response.StatusCode)
	assert.Empty(t, pool.recordedRequests())
}

func TestClient_RetryHelpersAndModes(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name    string
		attempt int
		want    attemptStrategy
	}{
		{name: "first", attempt: 1, want: attemptStrategy{}},
		{name: "fresh connection", attempt: 2, want: attemptStrategy{freshTCP: true}},
		{name: "exclude previous", attempt: 3, want: attemptStrategy{excludePrev: true}},
		{name: "fallback", attempt: defaultMaxAttempts, want: attemptStrategy{excludePrev: true, allowFallback: true}},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, strategyFor(tc.attempt))
		})
	}

	policy := clientTestRetryPolicy(defaultMaxAttempts)
	assert.Zero(t, policy.backoffFor(1))
	assert.Zero(t, policy.backoffFor(2))
	assert.True(t, shouldRetry(&http.Response{StatusCode: http.StatusBadGateway}, nil))
	assert.True(t, shouldRetry(&http.Response{StatusCode: http.StatusTooManyRequests}, nil))
	assert.False(t, shouldRetry(&http.Response{StatusCode: http.StatusNotFound}, nil))
	assert.False(t, shouldRetry(nil, context.Canceled))
	assert.False(t, shouldRetry(nil, context.DeadlineExceeded))
	assert.False(t, shouldRetry(nil, ErrPoolExhausted))
	assert.False(t, shouldRetry(nil, ErrDirectDialForbidden))
	assert.True(t, shouldRetry(nil, &net.DNSError{IsTimeout: true}))
	assert.True(t, shouldFallbackOnStatus(&http.Response{StatusCode: http.StatusForbidden}))
	assert.False(t, shouldFallbackOnStatus(&http.Response{StatusCode: http.StatusBadRequest}))
	assert.Equal(t, "vpn_only", ModeVPNOnly.String())
	assert.Equal(t, "public_first", ModePublicFirst.String())
	assert.Equal(t, "unknown", Mode(defaultMaxAttempts).String())
}

func TestClient_RewindAndBackoffFailures(t *testing.T) {
	t.Parallel()

	request := httptest.NewRequest(
		http.MethodPost,
		"http://upstream.test",
		io.NopCloser(strings.NewReader("body")),
	)
	require.Nil(t, request.GetBody)
	require.ErrorIs(t, rewindBody(request), ErrEgressUnavailable)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := waitBackoff(ctx, RetryPolicy{
		InitialBackoff:    time.Hour,
		MaxBackoff:        time.Hour,
		BackoffMultiplier: 1,
	}, 2)
	require.ErrorIs(t, err, context.Canceled)
}

func TestPreflight_ParsesAndRequiresQuorum(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "text/plain, application/json", r.Header.Get("Accept"))
		assert.Equal(t, "pr0xteus-preflight/1.0", r.Header.Get(headerUserAgent))

		switch r.URL.Path {
		case "/plain", "/json":
			_, err := w.Write([]byte("198.51.100.20\n"))
			assert.NoError(t, err)
		case "/different":
			_, err := w.Write([]byte(`{"ip":"198.51.100.21"}`))
			assert.NoError(t, err)
		default:
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	t.Cleanup(server.Close)

	ip, err := fetchAgreedPublicIP(
		context.Background(),
		server.Client(),
		[]string{server.URL + "/plain", server.URL + "/json"},
	)
	require.NoError(t, err)
	assert.Equal(t, "198.51.100.20", ip)

	_, err = fetchAgreedPublicIP(
		context.Background(),
		server.Client(),
		[]string{server.URL + "/plain", server.URL + "/different"},
	)
	require.ErrorIs(t, err, ErrPreflightFailed)
	assert.Equal(t, "198.51.100.22", parseIPEchoBody([]byte(`{"ip":"198.51.100.22"}`)))
	assert.Equal(t, "198.51.100.23", parseIPEchoBody([]byte("\n198.51.100.23\n")))
	assert.Empty(t, parseIPEchoBody([]byte("{}")))
	assert.True(t, IsPreflightFailed(err))
	assert.False(t, IsPreflightLeak(err))
}

func TestTransportBuilders_ApplyExpectedSafetySettings(t *testing.T) {
	t.Parallel()

	proxyURL := clientTestProxyURL(t, clientTestProxyOneHost)
	proxyTransport := buildTransport(proxyURL, true)
	t.Cleanup(proxyTransport.CloseIdleConnections)
	assert.Nil(t, proxyTransport.Proxy)
	assert.True(t, proxyTransport.DisableKeepAlives)
	assert.Equal(t, transportMaxIdleConns, proxyTransport.MaxIdleConns)
	assert.Equal(t, transportMaxIdleConnsPerHost, proxyTransport.MaxIdleConnsPerHost)

	directTransport := buildDirectTransport()
	t.Cleanup(directTransport.CloseIdleConnections)
	assert.NotNil(t, directTransport.DialContext)
	assert.Equal(t, directMaxIdleConns, directTransport.MaxIdleConns)
	assert.Equal(t, directIdleConnTimeout, directTransport.IdleConnTimeout)

	preflightTransport := buildPreflightProxyTransport(proxyURL)
	t.Cleanup(preflightTransport.CloseIdleConnections)
	assert.True(t, preflightTransport.DisableKeepAlives)
	assert.False(t, preflightTransport.ForceAttemptHTTP2)
}

func TestDirectTransport_DialsARealIPv4Listener(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)

	transport := buildDirectTransport()
	t.Cleanup(transport.CloseIdleConnections)
	response, err := (&http.Client{Transport: transport}).Get(server.URL)
	require.NoError(t, err)
	closeClientTestResponse(t, response)
	assert.Equal(t, http.StatusNoContent, response.StatusCode)
}

func clientTestRetryPolicy(maxAttempts int) RetryPolicy {
	return RetryPolicy{
		MaxAttempts:       maxAttempts,
		InitialBackoff:    0,
		MaxBackoff:        0,
		BackoffMultiplier: 1,
		Jitter:            0,
		UseFallbackPool:   true,
	}
}

func clientTestProxyResponse(t *testing.T, host string) *ProxyResponse {
	t.Helper()

	return &ProxyResponse{ProxyURL: clientTestProxyURL(t, host)}
}

func clientTestProxyURL(t *testing.T, host string) *url.URL {
	t.Helper()

	proxyURL, err := url.Parse("socks5://" + host)
	require.NoError(t, err)

	return proxyURL
}

func clientTestTransportFactory(
	t *testing.T,
	serverURL string,
	freshTCPAttempts *[]bool,
) TransportFactory {
	t.Helper()

	target := clientTestServerURL(t, serverURL)

	return func(_ *url.URL, freshTCP bool) *http.Transport {
		if freshTCPAttempts != nil {
			*freshTCPAttempts = append(*freshTCPAttempts, freshTCP)
		}

		return clientTestTransport(target)
	}
}

func clientTestDirectTransportFactory(
	t *testing.T,
	serverURL string,
) DirectTransportFactory {
	t.Helper()

	target := clientTestServerURL(t, serverURL)

	return func() *http.Transport {
		return clientTestTransport(target)
	}
}

func clientTestServerURL(t *testing.T, serverURL string) *url.URL {
	t.Helper()

	target, err := url.Parse(serverURL)
	require.NoError(t, err)

	return target
}

func clientTestTransport(target *url.URL) *http.Transport {
	return &http.Transport{
		DialContext: func(
			ctx context.Context,
			_ string,
			_ string,
		) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "tcp", target.Host)
		},
	}
}

func closeClientTestResponse(t *testing.T, response *http.Response) {
	t.Helper()
	require.NotNil(t, response)
	t.Cleanup(func() {
		assert.NoError(t, response.Body.Close())
	})
}
