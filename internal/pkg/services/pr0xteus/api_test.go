package pr0xteus

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/psyb0t/aichteeteapee"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testAPIToken = "test-api-token"

type apiTestSpawner struct {
	requests []SpawnRequest
	proxyURL *url.URL
}

func (s *apiTestSpawner) Spawn(
	_ context.Context, request SpawnRequest,
) (*Tunnel, error) {
	s.requests = append(s.requests, request)

	return &Tunnel{
		ContainerID: "test-cell",
		ConfName:    request.ConfName,
		ExitCountry: request.ExitCountry,
		ProxyURL:    s.proxyURL,
	}, nil
}

func (s *apiTestSpawner) Kill(_ context.Context, _ string) error {
	return nil
}

func (s *apiTestSpawner) ListChildren(_ context.Context) ([]CellHandle, error) {
	return nil, nil
}

func TestAPIServer_RejectsUnauthenticatedRequests(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name   string
		header string
	}{
		{name: "missing bearer"},
		{name: "wrong scheme", header: "Basic wrong"},
		{name: "wrong token", header: "Bearer wrong-token"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			server, spawner := newTestAPIServer(t)
			request := httptest.NewRequest(
				http.MethodPost,
				pathV1Proxies,
				bytes.NewBufferString(`{"country":"DE"}`),
			)
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set(aichteeteapee.HeaderNameAuthorization, tc.header)
			response := httptest.NewRecorder()

			server.ServeHTTP(response, request)

			assertErrorResponse(
				t,
				response,
				http.StatusUnauthorized,
				aichteeteapee.ErrorResponseUnauthorized,
			)
			assert.Empty(t, spawner.requests)
		})
	}
}

func TestAPIServer_RejectsInvalidProxyRequests(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string
		body string
	}{
		{name: "empty body"},
		{name: "unknown field", body: `{"country":"DE","wat":true}`},
		{name: "two selection modes", body: `{"country":"DE","pool":"western"}`},
		{name: "trailing JSON", body: `{"country":"DE"}{}`},
		{name: "non socks exclusion", body: `{"country":"DE","excludeProxy":"http://bad:80"}`},
		{name: "socks exclusion missing port", body: `{"country":"DE","excludeProxy":"socks5://bad"}`},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			server, spawner := newTestAPIServer(t)
			response := httptest.NewRecorder()

			server.ServeHTTP(
				response,
				newAuthenticatedRequest(t, http.MethodPost, pathV1Proxies, tc.body),
			)

			assertErrorResponse(
				t,
				response,
				http.StatusBadRequest,
				aichteeteapee.ErrorResponseValidationFailed,
			)
			assert.Empty(t, spawner.requests)
		})
	}

	t.Run("oversized body", func(t *testing.T) {
		t.Parallel()

		server, spawner := newTestAPIServer(t)
		body := `{"country":"DE","padding":"` +
			string(bytes.Repeat([]byte("x"), maxRequestBody)) + `"}`
		response := httptest.NewRecorder()

		server.ServeHTTP(
			response,
			newAuthenticatedRequest(t, http.MethodPost, pathV1Proxies, body),
		)

		assertErrorResponse(
			t,
			response,
			http.StatusBadRequest,
			aichteeteapee.ErrorResponseValidationFailed,
		)
		assert.Empty(t, spawner.requests)
	})
}

func TestAPIServer_AssignsCountryAndPool(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string
		body string
	}{
		{name: "country", body: `{"country":"DE"}`},
		{name: "explicit pool", body: `{"pool":"western"}`},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			server, spawner := newTestAPIServer(t)
			response := httptest.NewRecorder()

			server.ServeHTTP(
				response,
				newAuthenticatedRequest(t, http.MethodPost, pathV1Proxies, tc.body),
			)

			require.Equal(t, http.StatusOK, response.Code)
			require.Len(t, spawner.requests, 1)
			assert.Equal(t, "western", spawner.requests[0].Pool)

			var payload ProxyResponse
			require.NoError(t, json.NewDecoder(response.Body).Decode(&payload))
			assertProxyLeaseURL(t, payload.URL)
			assert.Equal(t, "western", payload.Pool)
			assert.Equal(t, "DE", payload.ExitCountry)
			assert.WithinDuration(t, time.Now().Add(defaultProxyLeaseTTL), payload.ExpiresAt, time.Second)
		})
	}
}

func TestAPIServer_ListsActiveProxies(t *testing.T) {
	t.Parallel()

	server, _ := newTestAPIServer(t)
	allocated := httptest.NewRecorder()
	server.ServeHTTP(
		allocated,
		newAuthenticatedRequest(t, http.MethodPost, pathV1Proxies, `{"country":"DE"}`),
	)
	require.Equal(t, http.StatusOK, allocated.Code)

	var allocation ProxyResponse
	require.NoError(t, json.NewDecoder(allocated.Body).Decode(&allocation))

	response := httptest.NewRecorder()
	server.ServeHTTP(
		response,
		newAuthenticatedRequest(t, http.MethodGet, pathV1Proxies+"?limit=1&offset=0", ""),
	)
	require.Equal(t, http.StatusOK, response.Code)

	var payload ProxyListResponse
	require.NoError(t, json.NewDecoder(response.Body).Decode(&payload))
	require.Len(t, payload.Proxies, 1)
	assert.Equal(t, 1, payload.Total)
	assert.Equal(t, allocation.URL, payload.Proxies[0].LastURL)
	assert.Equal(t, allocation.ExpiresAt, payload.Proxies[0].LastURLExpiresAt)
	assert.Equal(t, "western", payload.Proxies[0].Pool)
}

func TestAPIServer_ProxyInventoryPaginates(t *testing.T) {
	t.Parallel()

	manager := proxyInventoryTestManager(t, 3)
	server := NewAPIServer(manager, []byte(testAPIToken))

	first := httptest.NewRecorder()
	server.ServeHTTP(
		first,
		newAuthenticatedRequest(t, http.MethodGet, pathV1Proxies+"?limit=2", ""),
	)
	require.Equal(t, http.StatusOK, first.Code)
	var firstPage ProxyListResponse
	require.NoError(t, json.NewDecoder(first.Body).Decode(&firstPage))
	assert.Len(t, firstPage.Proxies, 2)
	assert.Equal(t, 3, firstPage.Total)
	assert.Equal(t, 0, firstPage.Offset)

	second := httptest.NewRecorder()
	server.ServeHTTP(
		second,
		newAuthenticatedRequest(t, http.MethodGet, pathV1Proxies+"?limit=2&offset=2", ""),
	)
	require.Equal(t, http.StatusOK, second.Code)
	var secondPage ProxyListResponse
	require.NoError(t, json.NewDecoder(second.Body).Decode(&secondPage))
	assert.Len(t, secondPage.Proxies, 1)
	assert.Equal(t, 3, secondPage.Total)
	assert.Equal(t, 2, secondPage.Offset)
	assert.NotEqual(t, firstPage.Proxies[0].Pool, secondPage.Proxies[0].Pool)
}

func TestAPIServer_AcceptsIssuedURLForExcludeProxy(t *testing.T) {
	t.Parallel()

	server, _ := newTestAPIServer(t)
	allocated := httptest.NewRecorder()
	server.ServeHTTP(
		allocated,
		newAuthenticatedRequest(t, http.MethodPost, pathV1Proxies, `{"country":"DE"}`),
	)
	require.Equal(t, http.StatusOK, allocated.Code)

	var previous ProxyResponse
	require.NoError(t, json.NewDecoder(allocated.Body).Decode(&previous))

	replacement := httptest.NewRecorder()
	server.ServeHTTP(
		replacement,
		newAuthenticatedRequest(
			t,
			http.MethodPost,
			pathV1Proxies,
			`{"country":"DE","excludeProxy":`+strconv.Quote(previous.URL)+`}`,
		),
	)
	assert.Equal(t, http.StatusServiceUnavailable, replacement.Code)
}

func TestAPIServer_RejectsInvalidProxyInventoryPagination(t *testing.T) {
	t.Parallel()

	server, _ := newTestAPIServer(t)
	for _, query := range []string{"?limit=0", "?limit=1001", "?offset=-1", "?limit=nope"} {
		response := httptest.NewRecorder()
		server.ServeHTTP(
			response,
			newAuthenticatedRequest(t, http.MethodGet, pathV1Proxies+query, ""),
		)
		assertErrorResponse(
			t,
			response,
			http.StatusBadRequest,
			aichteeteapee.ErrorResponseValidationFailed,
		)
	}
}

func proxyInventoryTestManager(t *testing.T, count int) *Manager {
	t.Helper()

	specs := make(map[string]PoolSpec, count)
	now := time.Now()
	for index := range count {
		name := "pool-" + strconv.Itoa(index)
		confName := "conf-" + strconv.Itoa(index)
		specs[name] = PoolSpec{
			Name:          name,
			Configs:       []string{confName},
			ExitCountries: map[string]string{confName: "DE"},
		}
	}

	manager := NewManager(
		Config{FailureCacheTTL: time.Minute, SpawnTimeout: time.Second},
		specs,
		&Router{countryToPool: map[string]string{}},
		&cellsTestSpawner{},
	)
	for name, state := range manager.Pools() {
		proxyURL, err := url.Parse("socks5://" + name + ":1080")
		require.NoError(t, err)
		state.setTunnel(&Tunnel{
			ContainerID: name,
			ConfName:    state.Spec.Configs[0],
			ProxyURL:    proxyURL,
			GatewayAddr: proxyURL.Host,
			State:       TunnelStateHot,
			Pool:        name,
			ExitCountry: "DE",
			SpawnedAt:   now,
			HealthyAt:   now,
			LastUsedAt:  now,
		})
	}

	return manager
}

// TestAPIServer_SuccessfulAcquireIncrementsAcquireMetric extends the
// success path covered by TestAPIServer_AssignsCountryAndPool with an
// assertion on TunnelAcquireTotal{pool,"ok"}. It intentionally does not call
// t.Parallel(): TunnelAcquireTotal is a package-level promauto counter
// shared across the whole test binary, so the delta must be read serially.
func TestAPIServer_SuccessfulAcquireIncrementsAcquireMetric(t *testing.T) {
	server, _ := newTestAPIServer(t)
	response := httptest.NewRecorder()

	before := counterValue(t, TunnelAcquireTotal, "western", metricOutcomeOK)

	server.ServeHTTP(
		response,
		newAuthenticatedRequest(t, http.MethodPost, pathV1Proxies, `{"country":"DE"}`),
	)

	require.Equal(t, http.StatusOK, response.Code)
	assert.Equal(t, before+1, counterValue(t, TunnelAcquireTotal, "western", metricOutcomeOK))
}

func TestAPIServer_ProtectsPoolStatus(t *testing.T) {
	t.Parallel()

	server, _ := newTestAPIServer(t)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, httptest.NewRequest(http.MethodGet, pathV1Pools, nil))

	assertErrorResponse(
		t,
		response,
		http.StatusUnauthorized,
		aichteeteapee.ErrorResponseUnauthorized,
	)
}

func TestAPIServer_ProtectsProxyInventory(t *testing.T) {
	t.Parallel()

	server, _ := newTestAPIServer(t)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, httptest.NewRequest(http.MethodGet, pathV1Proxies, nil))

	assertErrorResponse(
		t,
		response,
		http.StatusUnauthorized,
		aichteeteapee.ErrorResponseUnauthorized,
	)
}

func TestAPIServer_ListsPoolsAndMapsAcquireErrors(t *testing.T) {
	t.Parallel()

	server, _ := newTestAPIServer(t)

	t.Run("lists pools for an authenticated caller", func(t *testing.T) {
		t.Parallel()

		response := httptest.NewRecorder()
		server.ServeHTTP(
			response,
			newAuthenticatedRequest(t, http.MethodGet, pathV1Pools, ""),
		)

		require.Equal(t, http.StatusOK, response.Code)

		var payload map[string]json.RawMessage
		require.NoError(t, json.NewDecoder(response.Body).Decode(&payload))
		require.Contains(t, payload, "pools")
	})

	testCases := []struct {
		name     string
		err      error
		status   int
		response aichteeteapee.ErrorResponse
	}{
		{
			name:     "invalid selection",
			err:      ErrInvalidCountry,
			status:   http.StatusBadRequest,
			response: aichteeteapee.ErrorResponseValidationFailed,
		},
		{
			name:     "unknown pool",
			err:      ErrUnknownPool,
			status:   http.StatusBadRequest,
			response: aichteeteapee.ErrorResponseValidationFailed,
		},
		{
			name:     "exhausted pool",
			err:      ErrPoolExhausted,
			status:   http.StatusServiceUnavailable,
			response: aichteeteapee.ErrorResponseServiceUnavailable,
		},
		{
			name:     "unavailable pool",
			err:      ErrPoolUnavailable,
			status:   http.StatusServiceUnavailable,
			response: aichteeteapee.ErrorResponseServiceUnavailable,
		},
		{
			name:     "unexpected error",
			err:      errors.New("docker daemon interrupted"),
			status:   http.StatusInternalServerError,
			response: aichteeteapee.ErrorResponseInternalServerError,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			response := httptest.NewRecorder()
			server.writeAcquireError(context.Background(), response, tc.err)
			assertErrorResponse(t, response, tc.status, tc.response)
		})
	}
}

func TestIsCountryCode(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name    string
		country string
		want    bool
	}{
		{name: "uppercase two letters", country: "DE", want: true},
		{name: "lowercase two letters", country: "de", want: true},
		{name: "trims surrounding space", country: " de ", want: true},
		{name: "too short", country: "D"},
		{name: "too long", country: "DEU"},
		{name: "contains a digit", country: "D1"},
		{name: "empty", country: ""},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.want, isCountryCode(tc.country))
		})
	}
}

func newTestAPIServer(t *testing.T) (*APIServer, *apiTestSpawner) {
	t.Helper()

	proxyURL, err := url.Parse("socks5://test-cell:1080")
	require.NoError(t, err)

	spawner := &apiTestSpawner{proxyURL: proxyURL}
	specs := map[string]PoolSpec{
		"western": {
			Name:          "western",
			Configs:       []string{"de-frankfurt"},
			ExitCountries: map[string]string{"de-frankfurt": "DE"},
		},
	}
	router := &Router{countryToPool: map[string]string{"de": "western"}}
	manager := NewManager(
		Config{FailureCacheTTL: time.Minute, SpawnTimeout: time.Second},
		specs,
		router,
		spawner,
	)

	return NewAPIServer(manager, []byte(testAPIToken)), spawner
}

func newAuthenticatedRequest(
	t *testing.T, method, target, body string,
) *http.Request {
	t.Helper()

	request := httptest.NewRequest(method, target, bytes.NewBufferString(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(
		aichteeteapee.HeaderNameAuthorization,
		bearerScheme+testAPIToken,
	)

	return request
}

func assertErrorResponse(
	t *testing.T,
	response *httptest.ResponseRecorder,
	wantStatus int,
	wantBody aichteeteapee.ErrorResponse,
) {
	t.Helper()

	assert.Equal(t, wantStatus, response.Code)

	var body aichteeteapee.ErrorResponse
	require.NoError(t, json.NewDecoder(response.Body).Decode(&body))
	assert.Equal(t, wantBody, body)
}
