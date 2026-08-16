package pr0xteus

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/psyb0t/aichteeteapee"
	"github.com/psyb0t/pr0xteus/internal/pkg/cellproxy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func assertJSONContentType(t *testing.T, response *httptest.ResponseRecorder) {
	t.Helper()

	assert.Contains(
		t,
		response.Header().Get(aichteeteapee.HeaderNameContentType),
		aichteeteapee.ContentTypeJSON,
	)
}

func TestAPIServer_ProxyResponseFormat(t *testing.T) {
	t.Parallel()

	server, _ := newTestAPIServer(t)
	response := httptest.NewRecorder()
	server.ServeHTTP(
		response,
		newAuthenticatedRequest(t, http.MethodPost, pathV1Proxies, `{"country":"DE"}`),
	)

	require.Equal(t, http.StatusOK, response.Code)
	assertJSONContentType(t, response)

	body := response.Body.String()
	for _, key := range []string{`"url"`, `"pool"`, `"exitCountry"`} {
		assert.Contains(t, body, key, "proxy response must carry %s", key)
	}

	var payload ProxyResponse
	require.NoError(t, json.Unmarshal([]byte(body), &payload))
	assertProxyLeaseURL(t, payload.URL)
	assert.Equal(t, "western", payload.Pool)
	assert.Equal(t, "DE", payload.ExitCountry)
	assert.False(t, payload.ExpiresAt.IsZero())
}

func assertProxyLeaseURL(t *testing.T, raw string) {
	t.Helper()

	leaseURL, err := url.Parse(raw)
	require.NoError(t, err)
	assert.Equal(t, proxySchemeSOCKS5, leaseURL.Scheme)
	assert.Equal(t, defaultSOCKSPublicAddr, leaseURL.Host)
	require.NotNil(t, leaseURL.User)
	assert.NotEmpty(t, leaseURL.User.Username())
	password, ok := leaseURL.User.Password()
	require.True(t, ok)
	assert.NotEmpty(t, password)
}

func TestAPIServer_PoolsResponseFormat(t *testing.T) {
	t.Parallel()

	specs := map[string]PoolSpec{
		"western": {
			Name:          "western",
			Region:        "eastern_europe",
			Configs:       []string{"de-frankfurt"},
			ExitCountries: map[string]string{"de-frankfurt": "DE"},
		},
	}
	router := &Router{countryToPool: map[string]string{"de": "western"}}
	manager := NewManager(
		Config{FailureCacheTTL: time.Minute, SpawnTimeout: time.Second},
		specs,
		router,
		&cellsTestSpawner{},
	)

	proxyURL, err := url.Parse("socks5://cell-hot:1080")
	require.NoError(t, err)

	now := time.Now()
	manager.Pools()["western"].setTunnel(&Tunnel{
		ContainerID: "cell-hot",
		ConfName:    "de-frankfurt",
		ProxyURL:    proxyURL,
		State:       TunnelStateHot,
		Pool:        "western",
		ExitCountry: "DE",
		SpawnedAt:   now,
		HealthyAt:   now,
		LastUsedAt:  now,
	})

	api := NewAPIServer(manager, []byte(testAPIToken))
	response := httptest.NewRecorder()
	api.ServeHTTP(response, newAuthenticatedRequest(t, http.MethodGet, pathV1Pools, ""))

	require.Equal(t, http.StatusOK, response.Code)
	assertJSONContentType(t, response)

	body := response.Body.String()
	for _, key := range []string{
		`"name"`, `"region"`, `"confCount"`, `"recentlyFailed"`,
		`"tunnel"`, `"confName"`, `"proxyUrl"`, `"state"`, `"exitCountry"`,
	} {
		assert.Contains(t, body, key, "pools response must carry %s", key)
	}

	var payload struct {
		Pools []PoolView `json:"pools"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &payload))
	require.Len(t, payload.Pools, 1)

	pool := payload.Pools[0]
	assert.Equal(t, "western", pool.Name)
	assert.Equal(t, "eastern_europe", pool.Region)
	assert.Equal(t, 1, pool.ConfCount)
	assert.Equal(t, 0, pool.RecentlyFailed)
	require.NotNil(t, pool.Tunnel)
	assert.Equal(t, "de-frankfurt", pool.Tunnel.ConfName)
	assert.Equal(t, "socks5://cell-hot:1080", pool.Tunnel.ProxyURL)
	assert.Equal(t, TunnelStateHot, pool.Tunnel.State)
	assert.Equal(t, "DE", pool.Tunnel.ExitCountry)
}

func TestAPIServer_CellsResponseFormat(t *testing.T) {
	t.Parallel()

	server := cellControlServer(t, cellproxy.Status{
		ParentID:      "ctrl-1",
		UptimeSeconds: 12,
		Traffic: cellproxy.Stats{
			Requests: 5, BytesUp: 100, BytesDown: 2000, Active: 1,
			Destinations: []cellproxy.DestStat{
				{Destination: "example.com:443", Requests: 5, BytesUp: 100, BytesDown: 2000},
			},
		},
	})
	api := NewAPIServer(
		newCellsTestManager(t, &cellsTestSpawner{}, mustParseURL(t, server.URL)),
		[]byte(testAPIToken),
	)

	response := httptest.NewRecorder()
	api.ServeHTTP(response, newAuthenticatedRequest(t, http.MethodGet, pathV1Cells, ""))

	require.Equal(t, http.StatusOK, response.Code)
	assertJSONContentType(t, response)

	body := response.Body.String()
	for _, key := range []string{
		`"cells"`, `"containerId"`, `"pool"`, `"confName"`, `"state"`,
		`"traffic"`, `"requests"`, `"bytesUp"`, `"bytesDown"`,
		`"active"`, `"destinations"`, `"destination"`,
	} {
		assert.Contains(t, body, key, "cells response must carry %s", key)
	}

	var payload struct {
		Cells []CellView `json:"cells"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &payload))
	require.Len(t, payload.Cells, 1)

	cell := payload.Cells[0]
	assert.Equal(t, testCellContainerID, cell.ContainerID)
	assert.Equal(t, "western", cell.Pool)
	assert.Equal(t, "running", cell.State)
	require.NotNil(t, cell.Traffic)
	assert.Equal(t, int64(5), cell.Traffic.Requests)
	require.Len(t, cell.Traffic.Destinations, 1)
	assert.Equal(t, "example.com:443", cell.Traffic.Destinations[0].Destination)
}

func TestAPIServer_CellByIDResponseFormat(t *testing.T) {
	t.Parallel()

	server := cellControlServer(t, cellproxy.Status{Traffic: cellproxy.Stats{Requests: 2}})
	api := NewAPIServer(
		newCellsTestManager(t, &cellsTestSpawner{}, mustParseURL(t, server.URL)),
		[]byte(testAPIToken),
	)

	response := httptest.NewRecorder()
	api.ServeHTTP(response, newAuthenticatedRequest(
		t, http.MethodGet, pathV1Cells+"/"+testCellContainerID, "",
	))

	require.Equal(t, http.StatusOK, response.Code)
	assertJSONContentType(t, response)

	body := response.Body.String()
	// A single cell is not wrapped in a "cells" array.
	assert.False(t, strings.HasPrefix(strings.TrimSpace(body), `{"cells"`))

	var cell CellView
	require.NoError(t, json.Unmarshal([]byte(body), &cell))
	assert.Equal(t, testCellContainerID, cell.ContainerID)
	assert.Equal(t, "western", cell.Pool)
}
