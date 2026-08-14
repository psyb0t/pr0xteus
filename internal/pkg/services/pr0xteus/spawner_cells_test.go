package pr0xteus

import (
	"context"
	"net/netip"
	"testing"

	"github.com/moby/moby/api/types/container"
	mobynet "github.com/moby/moby/api/types/network"
	mobyclient "github.com/moby/moby/client"
	"github.com/psyb0t/ctxerrors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCellSpawner_BuildContainerConfigStampsParentAndControlPort(t *testing.T) {
	t.Parallel()

	spawner := &CellSpawner{cfg: Config{
		CellImage:       "psyb0t/pr0xteus:cell@sha256:aaaa",
		CellSocksPort:   1080,
		CellControlPort: 9090,
		ManagedScope:    "unit",
		ParentID:        "ctrl-parent",
	}}

	cfg := spawner.buildContainerConfig(
		SpawnRequest{Pool: "western", ConfName: "de-berlin"},
	)

	assert.Contains(t, cfg.ExposedPorts, mustParsePort(1080))
	assert.Contains(t, cfg.ExposedPorts, mustParsePort(9090))
	assert.Equal(t, "ctrl-parent", cfg.Labels[LabelParent])
	assert.Contains(t, cfg.Env, "PR0XTEUS_SOCKS5_PORT=1080")
	assert.Contains(t, cfg.Env, "PR0XTEUS_CELL_CONTROL_PORT=9090")
	assert.Contains(t, cfg.Env, "PR0XTEUS_PARENT_ID=ctrl-parent")
}

func TestCellSpawner_BuildContainerConfigWithoutParentID(t *testing.T) {
	t.Parallel()

	spawner := &CellSpawner{cfg: Config{
		CellSocksPort:   1080,
		CellControlPort: 9090,
	}}

	cfg := spawner.buildContainerConfig(SpawnRequest{Pool: "western"})

	_, hasParentLabel := cfg.Labels[LabelParent]
	assert.False(t, hasParentLabel)

	for _, entry := range cfg.Env {
		assert.NotContains(t, entry, "PR0XTEUS_PARENT_ID")
	}
}

func TestCellSpawner_ListChildrenResolvesControlURLFromDockerIP(t *testing.T) {
	t.Parallel()

	docker := &spawnerTestDockerClient{list: mobyclient.ContainerListResult{
		Items: []container.Summary{
			{
				ID:      "child-1",
				State:   "running",
				Created: 1_700_000_000,
				Labels: map[string]string{
					LabelPool: "western",
					LabelConf: "de-berlin",
				},
				NetworkSettings: &container.NetworkSettingsSummary{
					Networks: map[string]*mobynet.EndpointSettings{
						"cellnet": {IPAddress: netip.MustParseAddr("10.9.0.5")},
					},
				},
			},
		},
	}}
	spawner := &CellSpawner{cfg: Config{
		CellNetwork:     "cellnet",
		CellControlPort: 9090,
		ParentID:        "ctrl-parent",
	}, docker: docker}

	handles, err := spawner.ListChildren(context.Background())
	require.NoError(t, err)
	require.Len(t, handles, 1)
	assert.Equal(t, "child-1", handles[0].ContainerID)
	assert.Equal(t, "western", handles[0].Pool)
	assert.Equal(t, "de-berlin", handles[0].ConfName)
	assert.Equal(t, "running", handles[0].State)
	require.NotNil(t, handles[0].ControlURL)
	assert.Equal(t, "http", handles[0].ControlURL.Scheme)
	assert.Equal(t, "10.9.0.5:9090", handles[0].ControlURL.Host)
}

func TestIsRemovalInProgress(t *testing.T) {
	t.Parallel()

	assert.False(t, isRemovalInProgress(nil))
	assert.True(t, isRemovalInProgress(
		ctxerrors.New("removal of container abc is already in progress"),
	))
	assert.False(t, isRemovalInProgress(ctxerrors.New("some other docker error")))
}

func TestCellSpawner_ListChildrenNilControlURLInSmokeMode(t *testing.T) {
	t.Parallel()

	docker := &spawnerTestDockerClient{list: mobyclient.ContainerListResult{
		Items: []container.Summary{{ID: "child-1", State: "running"}},
	}}
	spawner := &CellSpawner{cfg: Config{CellControlPort: 9090}, docker: docker}

	handles, err := spawner.ListChildren(context.Background())
	require.NoError(t, err)
	require.Len(t, handles, 1)
	assert.Nil(t, handles[0].ControlURL)
}
