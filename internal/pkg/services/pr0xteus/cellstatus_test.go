package pr0xteus

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/psyb0t/pr0xteus/internal/pkg/cellproxy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// cellControlServer stands up an httptest server that answers the cellproxy
// control endpoints (/healthz + /status) the controller scrapes.
func cellControlServer(t *testing.T, status cellproxy.Status) *httptest.Server {
	t.Helper()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET /status", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(status))
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	return server
}

func mustParseURL(t *testing.T, raw string) *url.URL {
	t.Helper()

	parsed, err := url.Parse(raw)
	require.NoError(t, err)

	return parsed
}

func TestCellControlClient_StatusDecodesSnapshot(t *testing.T) {
	t.Parallel()

	want := cellproxy.Status{
		CellID:        "cell-abc",
		ParentID:      "ctrl-xyz",
		UptimeSeconds: 42,
		Traffic: cellproxy.Stats{
			Requests:  7,
			BytesUp:   1024,
			BytesDown: 2048,
			Active:    3,
			Destinations: []cellproxy.DestStat{
				{Destination: "example.com:443", Requests: 7, BytesUp: 1024, BytesDown: 2048},
			},
		},
	}

	server := cellControlServer(t, want)
	client := cellControlClient{http: server.Client()}

	got, err := client.Status(context.Background(), mustParseURL(t, server.URL))
	require.NoError(t, err)
	assert.Equal(t, want.CellID, got.CellID)
	assert.Equal(t, want.ParentID, got.ParentID)
	assert.Equal(t, want.Traffic.Requests, got.Traffic.Requests)
	assert.Equal(t, want.Traffic.Active, got.Traffic.Active)
	require.Len(t, got.Traffic.Destinations, 1)
	assert.Equal(t, "example.com:443", got.Traffic.Destinations[0].Destination)
}

func TestCellControlClient_StatusNilURL(t *testing.T) {
	t.Parallel()

	client := cellControlClient{http: http.DefaultClient}

	_, err := client.Status(context.Background(), nil)
	require.Error(t, err)
}

func TestCellControlClient_StatusNon200(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
		},
	))
	t.Cleanup(server.Close)

	client := cellControlClient{http: server.Client()}

	_, err := client.Status(context.Background(), mustParseURL(t, server.URL))
	require.Error(t, err)
}

func TestCellControlClient_Healthy(t *testing.T) {
	t.Parallel()

	server := cellControlServer(t, cellproxy.Status{})
	client := cellControlClient{http: server.Client()}

	assert.True(t, client.Healthy(context.Background(), mustParseURL(t, server.URL)))
	assert.False(t, client.Healthy(context.Background(), nil))
}

func TestCellControlClient_HealthyUnreachable(t *testing.T) {
	t.Parallel()

	client := cellControlClient{http: http.DefaultClient}

	// A closed port never accepts, so the probe must report unhealthy.
	assert.False(
		t, client.Healthy(context.Background(), mustParseURL(t, "http://127.0.0.1:1")),
	)
}
