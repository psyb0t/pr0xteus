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
}
