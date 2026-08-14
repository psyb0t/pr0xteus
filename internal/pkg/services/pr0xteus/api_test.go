package pr0xteus

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
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
			assert.Equal(t, "socks5://test-cell:1080", payload.URL)
			assert.Equal(t, "western", payload.Pool)
			assert.Equal(t, "DE", payload.ExitCountry)
		})
	}
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
