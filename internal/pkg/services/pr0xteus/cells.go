package pr0xteus

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/psyb0t/aichteeteapee"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/pr0xteus/internal/pkg/cellproxy"
)

const (
	pathV1Cells          = apiV1Prefix + "/cells"
	pathParamContainerID = "containerID"
	pathV1CellByID       = pathV1Cells + "/{" + pathParamContainerID + "}"
)

// CellView is the operator JSON projection of one running cell, discovered from
// docker: its identity and origin labels, docker's own container state, and the
// live traffic snapshot scraped from the cell's cellproxy /status. Traffic is
// nil (and StatusError set) when the controller could not reach the cell — e.g.
// host-loopback smoke mode, where cells expose no control address.
type CellView struct {
	ContainerID   string           `json:"containerId"`
	ParentID      string           `json:"parentId,omitempty"`
	Pool          string           `json:"pool"`
	ConfName      string           `json:"confName"`
	State         string           `json:"state"`
	ExitCountry   string           `json:"exitCountry,omitempty"`
	CreatedAt     time.Time        `json:"createdAt"`
	UptimeSeconds float64          `json:"uptimeSeconds,omitempty"`
	Traffic       *cellproxy.Stats `json:"traffic,omitempty"`
	StatusError   string           `json:"statusError,omitempty"`
}

// CellListResponse is the bounded live-cell collection returned by GET
// /v1/cells.
type CellListResponse struct {
	Cells  []CellView `json:"cells"`
	Limit  int        `json:"limit"`
	Offset int        `json:"offset"`
	Total  int        `json:"total"`
}

// Cells returns a view of every live cell, discovered from docker by the
// parent-id label and each enriched with its cellproxy /status traffic snapshot.
// Cells are sorted by container ID for stable output.
func (m *Manager) Cells(ctx context.Context) ([]CellView, error) {
	handles, err := m.spawner.ListChildren(ctx)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "list cells")
	}

	views := make([]CellView, 0, len(handles))
	for _, handle := range handles {
		views = append(views, m.cellView(ctx, handle))
	}

	sort.Slice(views, func(i, j int) bool {
		return views[i].ContainerID < views[j].ContainerID
	})

	return views, nil
}

// CellByID returns the view of a single live cell by its container ID.
func (m *Manager) CellByID(
	ctx context.Context, containerID string,
) (CellView, bool, error) {
	handles, err := m.spawner.ListChildren(ctx)
	if err != nil {
		return CellView{}, false, ctxerrors.Wrap(err, "list cells")
	}

	for _, handle := range handles {
		if handle.ContainerID == containerID {
			return m.cellView(ctx, handle), true, nil
		}
	}

	return CellView{}, false, nil
}

// DestroyCell kills the cell with the given container ID and clears any pool
// slot still holding it so the next request re-spawns. It returns
// commerr.ErrNotFound when docker reports no such child. The pool slot is
// cleared even when the docker kill errors, so a half-dead cell is never handed
// out again.
func (m *Manager) DestroyCell(ctx context.Context, containerID string) error {
	handles, err := m.spawner.ListChildren(ctx)
	if err != nil {
		return ctxerrors.Wrap(err, "list cells for destroy")
	}

	if !containsCell(handles, containerID) {
		return ctxerrors.Wrap(commerr.ErrNotFound, "cell not tracked")
	}

	killCtx, cancel := context.WithTimeout(ctx, killCtxTimeout)
	defer cancel()

	killErr := m.spawner.Kill(killCtx, containerID)

	if state := m.stateForContainer(containerID); state != nil {
		state.setTunnel(nil)
	}

	if killErr != nil {
		return ctxerrors.Wrap(killErr, "kill cell")
	}

	return nil
}

// containsCell reports whether handles include the given container ID.
func containsCell(handles []CellHandle, containerID string) bool {
	for _, handle := range handles {
		if handle.ContainerID == containerID {
			return true
		}
	}

	return false
}

// stateForContainer returns the pool state currently holding the given
// container ID, or nil when none does.
func (m *Manager) stateForContainer(containerID string) *PoolState {
	for _, state := range m.pools {
		tunnel := state.Snapshot()
		if tunnel != nil && tunnel.ContainerID == containerID {
			return state
		}
	}

	return nil
}

// cellView builds a CellView from a docker-discovered handle, scraping the
// cell's /status when a control address is available. A scrape failure degrades
// gracefully: the identity fields still render and StatusError explains the gap.
func (m *Manager) cellView(ctx context.Context, handle CellHandle) CellView {
	view := CellView{
		ContainerID: handle.ContainerID,
		Pool:        handle.Pool,
		ConfName:    handle.ConfName,
		State:       handle.State,
		ExitCountry: m.exitCountryFor(handle.Pool, handle.ConfName),
		CreatedAt:   handle.CreatedAt,
	}

	if handle.ControlURL == nil {
		return view
	}

	status, err := m.control.Status(ctx, handle.ControlURL)
	if err != nil {
		view.StatusError = err.Error()

		return view
	}

	traffic := status.Traffic
	view.ParentID = status.ParentID
	view.UptimeSeconds = status.UptimeSeconds
	view.Traffic = &traffic

	return view
}

// exitCountryFor resolves a cell's exit country from the operator-configured
// pool spec, falling back to the conventional <cc>-<location> conf-name prefix.
func (m *Manager) exitCountryFor(pool, conf string) string {
	if spec, ok := m.specs[pool]; ok {
		if country := spec.ExitCountries[conf]; country != "" {
			return country
		}
	}

	return exitCountryFromConf(conf)
}

// handleCells lists a bounded page of tracked cells with their live traffic
// snapshots.
func (s *APIServer) handleCells(w http.ResponseWriter, r *http.Request) {
	limit, offset, ok := collectionPage(w, r)
	if !ok {
		return
	}

	cells, err := s.mgr.Cells(r.Context())
	if err != nil {
		ctxscope.GetLogger(r.Context()).Error("list cells failed", "err", err)
		writeError(w, http.StatusInternalServerError, aichteeteapee.ErrorResponseInternalServerError)

		return
	}

	cells, offset, total := pageItems(cells, limit, offset)
	aichteeteapee.WriteJSON(w, http.StatusOK, CellListResponse{
		Cells:  cells,
		Limit:  limit,
		Offset: offset,
		Total:  total,
	})
}

// handleCell returns one cell by container ID, or 404 when it is not tracked.
func (s *APIServer) handleCell(w http.ResponseWriter, r *http.Request) {
	containerID := strings.TrimSpace(r.PathValue(pathParamContainerID))
	if containerID == "" {
		writeError(w, http.StatusBadRequest, aichteeteapee.ErrorResponseValidationFailed)

		return
	}

	view, found, err := s.mgr.CellByID(r.Context(), containerID)
	if err != nil {
		ctxscope.GetLogger(r.Context()).Error("get cell failed", "err", err)
		writeError(w, http.StatusInternalServerError, aichteeteapee.ErrorResponseInternalServerError)

		return
	}

	if !found {
		writeError(w, http.StatusNotFound, aichteeteapee.ErrorResponseNotFound)

		return
	}

	aichteeteapee.WriteJSON(w, http.StatusOK, view)
}

// handleDeleteCell destroys a cell on demand. 204 on success, 404 when the cell
// is not tracked, 500 when the docker kill itself failed.
func (s *APIServer) handleDeleteCell(w http.ResponseWriter, r *http.Request) {
	containerID := strings.TrimSpace(r.PathValue(pathParamContainerID))
	if containerID == "" {
		writeError(w, http.StatusBadRequest, aichteeteapee.ErrorResponseValidationFailed)

		return
	}

	logger := ctxscope.GetLogger(r.Context())

	err := s.mgr.DestroyCell(r.Context(), containerID)

	switch {
	case err == nil:
		logger.Info("cell destroyed on demand", "container", containerID)
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, commerr.ErrNotFound):
		writeError(w, http.StatusNotFound, aichteeteapee.ErrorResponseNotFound)
	default:
		logger.Error("cell destroy failed", "container", containerID, "err", err)
		writeError(w, http.StatusInternalServerError, aichteeteapee.ErrorResponseInternalServerError)
	}
}
