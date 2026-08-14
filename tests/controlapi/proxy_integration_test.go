//go:build integration

package controlapi

import (
	"context"
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

	require.NoError(t, infra.AssertProxyEgress(ctx, proxyURL.Host))

	// The control API sees the live cell, and its cellproxy /status reports the
	// traffic that just egressed through it.
	cells, err := infra.ListCells(ctx)
	require.NoError(t, err)
	require.Len(t, cells, 1)
	require.Equal(t, "integration", cells[0].Pool)
	require.NotEmpty(t, cells[0].ContainerID)
	require.NotNil(t, cells[0].Traffic, "cellproxy /status should be reachable")
	require.Positive(t, cells[0].Traffic.Requests, "egress request should be counted")

	// Destroying it on demand removes it from the pool.
	require.NoError(t, infra.DestroyCell(ctx, cells[0].ContainerID))

	remaining, err := infra.ListCells(ctx)
	require.NoError(t, err)
	require.Empty(t, remaining)
}
