//go:build integration

package controlapi

import (
	"context"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const proxyRequestTimeout = 3 * time.Minute

func TestControlAPI_AllocatesLiveWireGuardSOCKS5Proxy(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), proxyRequestTimeout)
	t.Cleanup(cancel)

	assignment, err := infra.AcquireProxy(ctx)
	require.NoError(t, err)
	require.Equal(t, "integration", assignment.Pool)
	require.Equal(t, "ZZ", assignment.ExitCountry)

	proxyURL, err := url.Parse(assignment.URL)
	require.NoError(t, err)
	require.Equal(t, "socks5", proxyURL.Scheme)
	require.NotEmpty(t, proxyURL.Host)
	require.NotNil(t, proxyURL.User)

	require.NoError(t, infra.AssertProxyEgress(ctx, assignment.URL))

	// GET /v1/pools — the operator pool view lists the configured pool.
	poolNames, err := infra.PoolNames(ctx)
	require.NoError(t, err)
	require.Contains(t, poolNames, "integration")

	// GET /v1/cells — the control API sees the live cell, and its cellproxy
	// /status reports the traffic that just egressed through it.
	cells, err := infra.ListCells(ctx)
	require.NoError(t, err)
	require.Len(t, cells, 1)
	require.Equal(t, "integration", cells[0].Pool)
	require.NotEmpty(t, cells[0].ContainerID)
	require.NotNil(t, cells[0].Traffic, "cellproxy /status should be reachable")
	require.Positive(t, cells[0].Traffic.Requests, "egress request should be counted")

	// GET /v1/cells/{id} — the single-cell view matches the list entry.
	one, err := infra.GetCell(ctx, cells[0].ContainerID)
	require.NoError(t, err)
	require.Equal(t, cells[0].ContainerID, one.ContainerID)
	require.Equal(t, "integration", one.Pool)

	// The auth gate rejects an unauthenticated request; the unauthenticated
	// metrics listener answers /healthz + /metrics.
	unauth, err := infra.UnauthenticatedCellsStatus(ctx)
	require.NoError(t, err)
	require.Equal(t, http.StatusUnauthorized, unauth)

	health, err := infra.HealthzStatus(ctx)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, health)

	metrics, err := infra.MetricsStatus(ctx)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, metrics)

	// DELETE /v1/cells/{id} — destroying it on demand removes it. Docker may
	// briefly report the container in its "removing" state, so poll until
	// discovery no longer sees it.
	require.NoError(t, infra.DestroyCell(ctx, cells[0].ContainerID))

	require.Eventually(t, func() bool {
		remaining, listErr := infra.ListCells(ctx)

		return listErr == nil && len(remaining) == 0
	}, 15*time.Second, 250*time.Millisecond, "destroyed cell should disappear")
}
