package pr0xteus

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/psyb0t/aichteeteapee"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/pr0xteus/internal/pkg/cellproxy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testCellContainerID = "cell-abc123"

// cellsTestSpawner records kills, can be made to fail them, and returns a
// configurable set of docker-discovered children.
type cellsTestSpawner struct {
	killErr  error
	killed   []string
	children []CellHandle
	listErr  error
}

func (s *cellsTestSpawner) Spawn(
	_ context.Context, request SpawnRequest,
) (*Tunnel, error) {
	return &Tunnel{ContainerID: testCellContainerID, ConfName: request.ConfName}, nil
}

func (s *cellsTestSpawner) Kill(_ context.Context, containerID string) error {
	s.killed = append(s.killed, containerID)

	return s.killErr
}

func (s *cellsTestSpawner) ListChildren(_ context.Context) ([]CellHandle, error) {
	return s.children, s.listErr
}

// newCellsTestManager wires a manager whose spawner reports one child (with the
// given control URL) both in docker discovery and in pool state, matching how a
// real running cell appears in both places.
func newCellsTestManager(
	t *testing.T, spawner *cellsTestSpawner, controlURL *url.URL,
) *Manager {
	t.Helper()

	spawner.children = []CellHandle{{
		ContainerID: testCellContainerID,
		Pool:        "western",
		ConfName:    "de-frankfurt",
		State:       "running",
		ControlURL:  controlURL,
	}}

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

	manager.Pools()["western"].setTunnel(&Tunnel{
		ContainerID: testCellContainerID,
		ConfName:    "de-frankfurt",
		State:       TunnelStateHot,
		Pool:        "western",
		ExitCountry: "DE",
		SpawnedAt:   time.Now(),
	})

	return manager
}

func TestManager_CellsScrapesTraffic(t *testing.T) {
	t.Parallel()

	server := cellControlServer(t, cellproxy.Status{
		ParentID:      "ctrl-1",
		UptimeSeconds: 12,
		Traffic:       cellproxy.Stats{Requests: 5, BytesUp: 100, Active: 2},
	})
	manager := newCellsTestManager(t, &cellsTestSpawner{}, mustParseURL(t, server.URL))

	cells, err := manager.Cells(context.Background())
	require.NoError(t, err)
	require.Len(t, cells, 1)
	assert.Equal(t, testCellContainerID, cells[0].ContainerID)
	assert.Equal(t, "ctrl-1", cells[0].ParentID)
	require.NotNil(t, cells[0].Traffic)
	assert.Equal(t, int64(5), cells[0].Traffic.Requests)
	assert.Equal(t, int64(2), cells[0].Traffic.Active)
	assert.Empty(t, cells[0].StatusError)
}

func TestManager_CellsWithoutControlURL(t *testing.T) {
	t.Parallel()

	manager := newCellsTestManager(t, &cellsTestSpawner{}, nil)

	cells, err := manager.Cells(context.Background())
	require.NoError(t, err)
	require.Len(t, cells, 1)
	assert.Nil(t, cells[0].Traffic)
	assert.Empty(t, cells[0].StatusError)
}

func TestManager_CellsStatusUnreachable(t *testing.T) {
	t.Parallel()

	manager := newCellsTestManager(
		t, &cellsTestSpawner{}, mustParseURL(t, "http://127.0.0.1:1"),
	)

	cells, err := manager.Cells(context.Background())
	require.NoError(t, err)
	require.Len(t, cells, 1)
	assert.Nil(t, cells[0].Traffic)
	assert.NotEmpty(t, cells[0].StatusError)
}

func TestManager_CellsEmptyWhenDockerReportsNoChildren(t *testing.T) {
	t.Parallel()

	specs := map[string]PoolSpec{
		"cold": {Name: "cold", Configs: []string{"de-frankfurt"}},
	}
	router := &Router{countryToPool: map[string]string{"de": "cold"}}
	manager := NewManager(
		Config{FailureCacheTTL: time.Minute, SpawnTimeout: time.Second},
		specs,
		router,
		&cellsTestSpawner{},
	)

	cells, err := manager.Cells(context.Background())
	require.NoError(t, err)
	assert.Empty(t, cells)

	_, ok, err := manager.CellByID(context.Background(), "anything")
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestManager_CellsHandlesDockerListError(t *testing.T) {
	t.Parallel()

	spawner := &cellsTestSpawner{listErr: ctxerrors.New("docker daemon down")}
	manager := NewManager(
		Config{},
		map[string]PoolSpec{"western": {Name: "western"}},
		&Router{},
		spawner,
	)

	_, err := manager.Cells(context.Background())
	require.Error(t, err)

	_, _, err = manager.CellByID(context.Background(), "anything")
	require.Error(t, err)

	require.Error(t, manager.DestroyCell(context.Background(), "anything"))
}

func TestManager_ExitCountryFallsBackToConfPrefix(t *testing.T) {
	t.Parallel()

	server := cellControlServer(t, cellproxy.Status{})
	spawner := &cellsTestSpawner{children: []CellHandle{{
		ContainerID: "c1",
		Pool:        "western",
		ConfName:    "us-newyork",
		State:       "running",
		ControlURL:  mustParseURL(t, server.URL),
	}}}
	manager := NewManager(
		Config{},
		map[string]PoolSpec{"western": {Name: "western"}},
		&Router{},
		spawner,
	)

	cells, err := manager.Cells(context.Background())
	require.NoError(t, err)
	require.Len(t, cells, 1)
	assert.Equal(t, "US", cells[0].ExitCountry)
}

func TestManager_CellByID(t *testing.T) {
	t.Parallel()

	server := cellControlServer(t, cellproxy.Status{})
	manager := newCellsTestManager(t, &cellsTestSpawner{}, mustParseURL(t, server.URL))

	view, ok, err := manager.CellByID(context.Background(), testCellContainerID)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, testCellContainerID, view.ContainerID)

	_, ok, err = manager.CellByID(context.Background(), "nope")
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestManager_DestroyCell(t *testing.T) {
	t.Parallel()

	spawner := &cellsTestSpawner{}
	manager := newCellsTestManager(t, spawner, nil)

	require.NoError(t, manager.DestroyCell(context.Background(), testCellContainerID))
	assert.Equal(t, []string{testCellContainerID}, spawner.killed)
	assert.Nil(t, manager.Pools()["western"].Snapshot())
}

func TestManager_DestroyCellNotFound(t *testing.T) {
	t.Parallel()

	manager := newCellsTestManager(t, &cellsTestSpawner{}, nil)

	err := manager.DestroyCell(context.Background(), "does-not-exist")
	require.ErrorIs(t, err, commerr.ErrNotFound)
}

func TestManager_DestroyCellKillErrorStillClearsSlot(t *testing.T) {
	t.Parallel()

	spawner := &cellsTestSpawner{killErr: ctxerrors.New("docker kill boom")}
	manager := newCellsTestManager(t, spawner, nil)

	err := manager.DestroyCell(context.Background(), testCellContainerID)
	require.Error(t, err)
	assert.Nil(t, manager.Pools()["western"].Snapshot())
}

func newCellsTestAPIServer(
	t *testing.T, spawner *cellsTestSpawner, controlURL *url.URL,
) *APIServer {
	t.Helper()

	return NewAPIServer(
		newCellsTestManager(t, spawner, controlURL), []byte(testAPIToken),
	)
}

func TestAPIServer_ListCells(t *testing.T) {
	t.Parallel()

	server := cellControlServer(t, cellproxy.Status{Traffic: cellproxy.Stats{Requests: 3}})
	api := newCellsTestAPIServer(t, &cellsTestSpawner{}, mustParseURL(t, server.URL))

	request := newAuthenticatedRequest(t, http.MethodGet, pathV1Cells, "")
	response := httptest.NewRecorder()
	api.ServeHTTP(response, request)

	require.Equal(t, http.StatusOK, response.Code)

	var body CellListResponse
	require.NoError(t, json.NewDecoder(response.Body).Decode(&body))
	require.Len(t, body.Cells, 1)
	assert.Equal(t, testCellContainerID, body.Cells[0].ContainerID)
	assert.Equal(t, 1, body.Total)
	assert.Equal(t, defaultProxyLimit, body.Limit)
}

func TestAPIServer_CellInventoryPaginates(t *testing.T) {
	t.Parallel()

	spawner := &cellsTestSpawner{}
	api := newCellsTestAPIServer(t, spawner, nil)
	spawner.children = []CellHandle{
		{ContainerID: "cell-1", Pool: "western", ConfName: "de-frankfurt", State: "running"},
		{ContainerID: "cell-2", Pool: "western", ConfName: "de-frankfurt", State: "running"},
		{ContainerID: "cell-3", Pool: "western", ConfName: "de-frankfurt", State: "running"},
	}

	response := httptest.NewRecorder()
	api.ServeHTTP(
		response,
		newAuthenticatedRequest(t, http.MethodGet, pathV1Cells+"?limit=2&offset=2", ""),
	)

	require.Equal(t, http.StatusOK, response.Code)
	var body CellListResponse
	require.NoError(t, json.NewDecoder(response.Body).Decode(&body))
	require.Len(t, body.Cells, 1)
	assert.Equal(t, "cell-3", body.Cells[0].ContainerID)
	assert.Equal(t, 2, body.Offset)
	assert.Equal(t, 3, body.Total)
}

func TestAPIServer_CellDiscoveryFailureIsInternalError(t *testing.T) {
	t.Parallel()

	spawner := &cellsTestSpawner{listErr: ctxerrors.New("docker daemon down")}
	api := newCellsTestAPIServer(t, spawner, nil)

	for _, path := range []string{pathV1Cells, pathV1Cells + "/" + testCellContainerID} {
		response := httptest.NewRecorder()
		api.ServeHTTP(response, newAuthenticatedRequest(t, http.MethodGet, path, ""))
		assertErrorResponse(
			t,
			response,
			http.StatusInternalServerError,
			aichteeteapee.ErrorResponseInternalServerError,
		)
	}
}

func TestAPIServer_GetCell(t *testing.T) {
	t.Parallel()

	server := cellControlServer(t, cellproxy.Status{})
	api := newCellsTestAPIServer(t, &cellsTestSpawner{}, mustParseURL(t, server.URL))

	found := newAuthenticatedRequest(
		t, http.MethodGet, pathV1Cells+"/"+testCellContainerID, "",
	)
	foundResp := httptest.NewRecorder()
	api.ServeHTTP(foundResp, found)
	assert.Equal(t, http.StatusOK, foundResp.Code)

	missing := newAuthenticatedRequest(t, http.MethodGet, pathV1Cells+"/ghost", "")
	missingResp := httptest.NewRecorder()
	api.ServeHTTP(missingResp, missing)
	assertErrorResponse(
		t, missingResp, http.StatusNotFound, aichteeteapee.ErrorResponseNotFound,
	)
}

func TestAPIServer_DeleteCell(t *testing.T) {
	t.Parallel()

	t.Run("success", func(t *testing.T) {
		t.Parallel()

		api := newCellsTestAPIServer(t, &cellsTestSpawner{}, nil)
		request := newAuthenticatedRequest(
			t, http.MethodDelete, pathV1Cells+"/"+testCellContainerID, "",
		)
		response := httptest.NewRecorder()
		api.ServeHTTP(response, request)
		assert.Equal(t, http.StatusNoContent, response.Code)
	})

	t.Run("not found", func(t *testing.T) {
		t.Parallel()

		api := newCellsTestAPIServer(t, &cellsTestSpawner{}, nil)
		request := newAuthenticatedRequest(t, http.MethodDelete, pathV1Cells+"/ghost", "")
		response := httptest.NewRecorder()
		api.ServeHTTP(response, request)
		assertErrorResponse(
			t, response, http.StatusNotFound, aichteeteapee.ErrorResponseNotFound,
		)
	})

	t.Run("kill failure is 500", func(t *testing.T) {
		t.Parallel()

		spawner := &cellsTestSpawner{killErr: ctxerrors.New("docker kill boom")}
		api := newCellsTestAPIServer(t, spawner, nil)
		request := newAuthenticatedRequest(
			t, http.MethodDelete, pathV1Cells+"/"+testCellContainerID, "",
		)
		response := httptest.NewRecorder()
		api.ServeHTTP(response, request)
		assertErrorResponse(
			t, response, http.StatusInternalServerError,
			aichteeteapee.ErrorResponseInternalServerError,
		)
	})
}
