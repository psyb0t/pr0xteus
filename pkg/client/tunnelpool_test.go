package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/psyb0t/aichteeteapee"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	tunnelPoolClientDeadline     = 10 * time.Millisecond
	tunnelPoolServerResponseWait = 100 * time.Millisecond
)

func TestTunnelPoolClient_SendsAuthenticatedCountryRequest(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/v1/proxies", r.URL.Path)
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
		assert.Equal(t, "Bearer test-token", r.Header.Get("Authorization"))

		var request tunnelPoolWireRequest
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		assert.Equal(t, "DE", request.Country)
		assert.Empty(t, request.Pool)

		w.Header().Set("Content-Type", "application/json")
		_, err := w.Write([]byte(`{"url":"socks5://cell:1080","pool":"western","exitCountry":"DE","exitIP":"203.0.113.10"}`))
		assert.NoError(t, err)
	}))
	t.Cleanup(server.Close)

	client := NewTunnelPoolClient(server.URL, 0, WithBearerToken("test-token"))
	response, err := client.ProxyForCountry(context.Background(), ProxyRequest{Country: "DE"})
	require.NoError(t, err)
	require.NotNil(t, response.ProxyURL)
	assert.Equal(t, "socks5://cell:1080", response.ProxyURL.String())
	assert.Equal(t, "western", response.Pool)
	assert.Equal(t, "DE", response.ExitCountry)
	assert.Equal(t, "203.0.113.10", response.ExitIP)
}

func TestTunnelPoolClient_PoolOverrideOmitsCountry(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request tunnelPoolWireRequest
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		assert.Equal(t, "western", request.Pool)
		assert.Empty(t, request.Country)

		w.Header().Set("Content-Type", "application/json")
		_, err := w.Write([]byte(`{"url":"socks5://cell:1080","pool":"western","exitCountry":"DE"}`))
		assert.NoError(t, err)
	}))
	t.Cleanup(server.Close)

	client := NewTunnelPoolClient(server.URL, 0, WithPool("western"))
	response, err := client.ProxyForCountry(
		context.Background(), ProxyRequest{Country: "DE"},
	)
	require.NoError(t, err)
	assert.Equal(t, "western", response.Pool)
}

func TestTunnelPoolClient_RejectsInvalidResponses(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name   string
		status int
		body   string
	}{
		{name: "malformed JSON", status: http.StatusOK, body: "{"},
		{name: "empty URL", status: http.StatusOK, body: `{"pool":"western"}`},
		{name: "malformed proxy URL", status: http.StatusOK, body: `{"url":"socks5://%zz"}`},
		{name: "server failure", status: http.StatusServiceUnavailable, body: "unavailable"},
		{
			name:   "response exceeds body cap",
			status: http.StatusOK,
			body:   `{"url":"socks5://cell:1080","padding":"` + strings.Repeat("x", 1<<16) + `"}`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, err := w.Write([]byte(tc.body))
				assert.NoError(t, err)
			}))
			t.Cleanup(server.Close)

			client := NewTunnelPoolClient(server.URL, 0)
			response, err := client.ProxyForCountry(
				context.Background(), ProxyRequest{Country: "DE"},
			)

			require.ErrorIs(t, err, ErrEgressUnavailable)
			assert.Nil(t, response)
		})
	}
}

func TestTunnelPoolClient_MapsPoolExhaustedResponse(t *testing.T) {
	t.Parallel()

	envelope, err := json.Marshal(aichteeteapee.ErrorResponseServiceUnavailable)
	require.NoError(t, err)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, writeErr := w.Write(envelope)
		assert.NoError(t, writeErr)
	}))
	t.Cleanup(server.Close)

	client := NewTunnelPoolClient(server.URL, 0)
	response, err := client.ProxyForCountry(
		context.Background(), ProxyRequest{Country: "DE"},
	)

	require.ErrorIs(t, err, ErrPoolExhausted)
	assert.Nil(t, response)
}

func TestTunnelPoolClient_ExposesConfiguredPoolAndDoer(t *testing.T) {
	t.Parallel()

	doer := &http.Client{}
	client := NewTunnelPoolClient(
		"http://pr0xteus.test",
		0,
		WithPool("western"),
		WithHTTPClient(doer),
	)

	assert.Equal(t, "western", client.PoolName())
	assert.Same(t, doer, client.doer)
}

func TestTunnelPoolClient_MapsRequestAndTransportFailures(t *testing.T) {
	t.Parallel()

	t.Run("rejects an invalid control API URL", func(t *testing.T) {
		t.Parallel()

		client := NewTunnelPoolClient("://invalid-url", 0)
		response, err := client.ProxyForCountry(
			context.Background(), ProxyRequest{Country: clientTestCountry},
		)
		require.ErrorIs(t, err, ErrEgressUnavailable)
		assert.Nil(t, response)
	})

	t.Run("wraps a transport failure", func(t *testing.T) {
		t.Parallel()

		client := NewTunnelPoolClient(
			"http://pr0xteus.test",
			0,
			WithHTTPClient(&http.Client{
				Transport: tunnelPoolRoundTripper(func(*http.Request) (*http.Response, error) {
					return nil, errors.New("controller unavailable")
				}),
			}),
		)
		response, err := client.ProxyForCountry(
			context.Background(), ProxyRequest{Country: clientTestCountry},
		)
		require.ErrorIs(t, err, ErrEgressUnavailable)
		assert.Nil(t, response)
	})
}

func TestTunnelPoolClient_EnforcesTheConfiguredDeadline(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(tunnelPoolServerResponseWait)
		w.WriteHeader(http.StatusGatewayTimeout)
	}))
	t.Cleanup(server.Close)

	client := NewTunnelPoolClient(server.URL, tunnelPoolClientDeadline)
	response, err := client.ProxyForCountry(
		context.Background(), ProxyRequest{Country: clientTestCountry},
	)
	require.ErrorIs(t, err, ErrEgressUnavailable)
	assert.Nil(t, response)
}

func TestTunnelPoolClient_TransmitsRetryRoutingControls(t *testing.T) {
	t.Parallel()

	excludedProxy := "socks5://previous-cell.test:1080"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request tunnelPoolWireRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		assert.Equal(t, excludedProxy, request.ExcludeProxy)
		assert.True(t, request.FallbackOK)

		w.WriteHeader(http.StatusServiceUnavailable)
		_, err := w.Write([]byte(strings.Repeat("x", 201)))
		require.NoError(t, err)
	}))
	t.Cleanup(server.Close)

	excludeURL, err := url.Parse(excludedProxy)
	require.NoError(t, err)
	client := NewTunnelPoolClient(server.URL, 0)
	response, err := client.ProxyForCountry(context.Background(), ProxyRequest{
		Country:      clientTestCountry,
		ExcludeProxy: excludeURL,
		FallbackOK:   true,
	})
	require.ErrorIs(t, err, ErrEgressUnavailable)
	assert.ErrorContains(t, err, "…")
	assert.Nil(t, response)
}

func TestTunnelPoolClient_FailsClosedWhenTheResponseBodyCannotBeRead(t *testing.T) {
	t.Parallel()

	client := NewTunnelPoolClient(
		"http://pr0xteus.test",
		0,
		WithHTTPClient(&http.Client{
			Transport: tunnelPoolRoundTripper(func(*http.Request) (*http.Response, error) {
				return &http.Response{
					Body:       tunnelPoolReadErrorBody{},
					StatusCode: http.StatusOK,
				}, nil
			}),
		}),
	)
	response, err := client.ProxyForCountry(
		context.Background(), ProxyRequest{Country: clientTestCountry},
	)
	require.ErrorIs(t, err, ErrEgressUnavailable)
	assert.Nil(t, response)
}

type tunnelPoolRoundTripper func(*http.Request) (*http.Response, error)

func (roundTrip tunnelPoolRoundTripper) RoundTrip(
	request *http.Request,
) (*http.Response, error) {
	return roundTrip(request)
}

type tunnelPoolReadErrorBody struct{}

func (tunnelPoolReadErrorBody) Read([]byte) (int, error) {
	return 0, errors.New("response body interrupted")
}

func (tunnelPoolReadErrorBody) Close() error { return nil }
