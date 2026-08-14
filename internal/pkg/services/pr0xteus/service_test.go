package pr0xteus

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNew_WiresAValidatedServiceFromOperatorFiles(t *testing.T) {
	// t.Setenv changes process state and cannot run in parallel.
	configureValidEnvironment(t)

	dir := t.TempDir()
	bundleDir := filepath.Join(dir, "wireguard")
	require.NoError(t, os.Mkdir(bundleDir, 0o700))
	writeRuntimeFixture(t, filepath.Join(bundleDir, "de-berlin.conf"), "[Interface]\n")

	poolsPath := filepath.Join(dir, "pools.yaml")
	writeRuntimeFixture(t, poolsPath, `pools:
  western:
    region: eu
    purpose: test
    configs: [de-berlin]
    exit_countries:
      de-berlin: DE
`)
	routingPath := filepath.Join(dir, "egress-routing.yaml")
	writeRuntimeFixture(t, routingPath, `country_to_pool:
  DE: western
default_pool: western
`)
	t.Setenv("TUNNEL_POOL_POOLS_FILE", poolsPath)
	t.Setenv("TUNNEL_POOL_BUNDLE_DIR", bundleDir)
	t.Setenv("TUNNEL_POOL_ROUTING_FILE", routingPath)
	t.Setenv("PR0XTEUS_API_TOKEN", "test-only-token")

	service, err := New()
	require.NoError(t, err)
	assert.Equal(t, ServiceName, service.Name())
	assert.IsType(t, &CellSpawner{}, service.spawner)
	assert.Equal(t, []string{"western"}, []string{service.mgr.Views()[0].Name})
	require.NoError(t, service.Stop(context.Background()))
}
