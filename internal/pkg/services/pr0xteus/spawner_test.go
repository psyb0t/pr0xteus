package pr0xteus

import (
	"context"
	"net/netip"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	mobynet "github.com/moby/moby/api/types/network"
	mobyclient "github.com/moby/moby/client"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxscope"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type spawnerTestDockerClient struct {
	createResult mobyclient.ContainerCreateResult
	createErr    error
	startErr     error
	stopErr      error
	removeErr    error
	inspect      mobyclient.ContainerInspectResult
	inspectErr   error
	list         mobyclient.ContainerListResult
	listErr      error

	createOptions []mobyclient.ContainerCreateOptions
	listOptions   []mobyclient.ContainerListOptions
	started       []string
	stopped       []string
	removed       []string
}

func (d *spawnerTestDockerClient) ContainerCreate(
	_ context.Context,
	options mobyclient.ContainerCreateOptions,
) (mobyclient.ContainerCreateResult, error) {
	d.createOptions = append(d.createOptions, options)

	return d.createResult, d.createErr
}

func (d *spawnerTestDockerClient) ContainerStart(
	_ context.Context,
	containerID string,
	_ mobyclient.ContainerStartOptions,
) (mobyclient.ContainerStartResult, error) {
	d.started = append(d.started, containerID)

	return mobyclient.ContainerStartResult{}, d.startErr
}

func (d *spawnerTestDockerClient) ContainerLogs(
	_ context.Context,
	_ string,
	_ mobyclient.ContainerLogsOptions,
) (mobyclient.ContainerLogsResult, error) {
	return nil, ctxerrors.New("container logs are not configured")
}

func (d *spawnerTestDockerClient) ContainerStop(
	_ context.Context,
	containerID string,
	_ mobyclient.ContainerStopOptions,
) (mobyclient.ContainerStopResult, error) {
	d.stopped = append(d.stopped, containerID)

	return mobyclient.ContainerStopResult{}, d.stopErr
}

func (d *spawnerTestDockerClient) ContainerRemove(
	_ context.Context,
	containerID string,
	_ mobyclient.ContainerRemoveOptions,
) (mobyclient.ContainerRemoveResult, error) {
	d.removed = append(d.removed, containerID)

	return mobyclient.ContainerRemoveResult{}, d.removeErr
}

func (d *spawnerTestDockerClient) ContainerInspect(
	_ context.Context,
	_ string,
	_ mobyclient.ContainerInspectOptions,
) (mobyclient.ContainerInspectResult, error) {
	return d.inspect, d.inspectErr
}

func (d *spawnerTestDockerClient) ContainerList(
	_ context.Context,
	options mobyclient.ContainerListOptions,
) (mobyclient.ContainerListResult, error) {
	d.listOptions = append(d.listOptions, options)

	return d.list, d.listErr
}

func TestCellSpawner_BuildHostConfigForPrivateNetwork(t *testing.T) {
	t.Parallel()

	spawner := &CellSpawner{
		cfg: Config{
			CellImage:     "psyb0t/pr0xteus:cell-v0.1.0@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			CellNetwork:   "pr0xteus_egress",
			CellSocksPort: 1080,
			ManagedScope:  "test-scope",
		},
	}
	hostConfig := spawner.buildHostConfig("/tmp/selected.conf")

	assert.True(t, hostConfig.AutoRemove)
	assert.Equal(
		t,
		[]string{
			cellCapabilityNetAdmin,
			cellCapabilitySetGID,
			cellCapabilitySetUID,
		},
		hostConfig.CapAdd,
	)
	assert.Equal(t, []string{"ALL"}, hostConfig.CapDrop)
	require.NotNil(t, hostConfig.Init)
	assert.True(t, *hostConfig.Init)
	assert.Equal(
		t,
		[]string{"/tmp/selected.conf:/wgconf/wg0.conf:ro"},
		hostConfig.Binds,
	)
	assert.Equal(t, int64(cellMemoryLimitBytes), hostConfig.Resources.Memory)
	assert.Equal(t, int64(cellNanoCPUs), hostConfig.Resources.NanoCPUs)
	require.NotNil(t, hostConfig.Resources.PidsLimit)
	assert.Equal(t, int64(cellPIDsLimit), *hostConfig.Resources.PidsLimit)
	require.Len(t, hostConfig.Resources.Devices, 1)
	assert.Equal(t, "/dev/net/tun", hostConfig.Resources.Devices[0].PathOnHost)
	assert.Equal(t, cellLogMaxSize, hostConfig.LogConfig.Config["max-size"])
	assert.Equal(t, cellLogMaxFiles, hostConfig.LogConfig.Config["max-file"])
	assert.Empty(t, hostConfig.PortBindings)
	assert.Equal(t, spawner.cfg.CellNetwork, string(hostConfig.NetworkMode))

	networkingConfig := spawner.buildNetworkingConfig()
	require.NotNil(t, networkingConfig)
	assert.Contains(t, networkingConfig.EndpointsConfig, spawner.cfg.CellNetwork)
}

func TestCellSpawner_BuildsLoopbackContainerConfiguration(t *testing.T) {
	t.Parallel()

	spawner := &CellSpawner{cfg: Config{
		CellImage:     "psyb0t/pr0xteus:cell-v0.1.0@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		CellSocksPort: 1080,
	}}
	request := SpawnRequest{Pool: "western", ConfName: "de-berlin"}
	containerConfig := spawner.buildContainerConfig(request)
	hostConfig := spawner.buildHostConfig("/tmp/de-berlin.conf")

	assert.Equal(t, spawner.cfg.CellImage, containerConfig.Image)
	assert.Equal(t, "true", containerConfig.Labels[LabelManaged])
	assert.Equal(t, request.Pool, containerConfig.Labels[LabelPool])
	assert.Equal(t, request.ConfName, containerConfig.Labels[LabelConf])
	assert.Equal(t, spawner.cfg.ManagedScope, containerConfig.Labels[LabelScope])
	assert.Contains(t, containerConfig.ExposedPorts, mustParsePort(spawner.cfg.CellSocksPort))
	require.Len(t, hostConfig.PortBindings, 1)
	bindings := hostConfig.PortBindings[mustParsePort(spawner.cfg.CellSocksPort)]
	require.Len(t, bindings, 1)
	assert.Equal(t, loopbackBindAddr, bindings[0].HostIP)
	assert.Empty(t, hostConfig.NetworkMode)
	assert.Nil(t, spawner.buildNetworkingConfig())
}

func TestCellSpawner_MapsCreateAndStartFailures(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name       string
		docker     *spawnerTestDockerClient
		wantStops  []string
		wantRemove []string
	}{
		{
			name: "create failure does not attempt cleanup",
			docker: &spawnerTestDockerClient{
				createErr: ctxerrors.New("daemon unavailable"),
			},
		},
		{
			name: "start failure cleans exact created container",
			docker: &spawnerTestDockerClient{
				createResult: mobyclient.ContainerCreateResult{ID: "created-cell"},
				startErr:     ctxerrors.New("start rejected"),
			},
			wantStops:  []string{"created-cell"},
			wantRemove: []string{"created-cell"},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			spawner := newSpawnerTestSubject(tc.docker)
			tunnel, err := spawner.Spawn(context.Background(), SpawnRequest{
				Pool:      "western",
				ConfName:  "de-berlin",
				BundleDir: "/bundle",
			})
			require.ErrorIs(t, err, ErrSpawnFailed)
			assert.Nil(t, tunnel)
			assert.Equal(t, tc.wantStops, tc.docker.stopped)
			assert.Equal(t, tc.wantRemove, tc.docker.removed)
		})
	}
}

func TestCellSpawner_KillTreatsMissingContainerAsSuccess(t *testing.T) {
	t.Parallel()

	spawner := newSpawnerTestSubject(&spawnerTestDockerClient{
		stopErr:   ctxerrors.New("No such container"),
		removeErr: ctxerrors.New("No such container"),
	})
	err := spawner.Kill(context.Background(), "missing-cell")
	require.NoError(t, err)
}

func TestCellSpawner_KillToleratesAutoRemoveRace(t *testing.T) {
	t.Parallel()

	// AutoRemove: true means the stop already triggered docker's own removal, so
	// the explicit remove finds one in progress. Kill must treat that as done.
	spawner := newSpawnerTestSubject(&spawnerTestDockerClient{
		removeErr: ctxerrors.New(
			"removal of container abc is already in progress",
		),
	})
	err := spawner.Kill(context.Background(), "abc")
	require.NoError(t, err)
}

func TestDetachedCleanupContext_PreservesScopeAfterCancellation(t *testing.T) {
	t.Parallel()

	ctx := ctxscope.Set(
		context.Background(),
		ctxscope.Attr("request_id", "test-request-id"),
	)
	ctx, cancelParent := context.WithCancel(ctx)
	cancelParent()
	t.Cleanup(cancelParent)

	cleanupCtx, cancelCleanup := detachedCleanupContext(ctx)
	t.Cleanup(cancelCleanup)

	assert.ErrorIs(t, ctx.Err(), context.Canceled)
	assert.NoError(t, cleanupCtx.Err())
	assert.Equal(t, "test-request-id", ctxscope.Get(cleanupCtx)["request_id"])
}

func TestCellSpawner_ResolvesAddressAndTimeout(t *testing.T) {
	t.Parallel()

	t.Run("uses private docker DNS when network is configured", func(t *testing.T) {
		t.Parallel()

		spawner := newSpawnerTestSubject(&spawnerTestDockerClient{})
		spawner.cfg.CellNetwork = "pr0xteus_egress"
		address, err := spawner.resolveSocksAddr(
			context.Background(),
			"cell-id",
			"cell-name",
		)
		require.NoError(t, err)
		assert.Equal(t, "cell-name:1080", address)
	})

	t.Run("uses inspected loopback binding without a network", func(t *testing.T) {
		t.Parallel()

		port := mustParsePort(1080)
		docker := &spawnerTestDockerClient{inspect: mobyclient.ContainerInspectResult{
			Container: container.InspectResponse{NetworkSettings: &container.NetworkSettings{
				Ports: mobynet.PortMap{
					port: {{HostIP: netip.MustParseAddr("127.0.0.1"), HostPort: "41080"}},
				},
			}},
		}}
		spawner := newSpawnerTestSubject(docker)
		address, err := spawner.resolveSocksAddr(
			context.Background(),
			"cell-id",
			"cell-name",
		)
		require.NoError(t, err)
		assert.Equal(t, "127.0.0.1:41080", address)
	})

	t.Run("cancelled readiness wait returns the timeout sentinel", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		spawner := newSpawnerTestSubject(&spawnerTestDockerClient{})
		tunnel, err := spawner.waitReady(ctx, "cell-id", "cell-name", SpawnRequest{})
		require.ErrorIs(t, err, ErrSpawnTimeout)
		assert.Nil(t, tunnel)
	})
}

func TestCellSpawner_ReapsOnlyUntrackedEligibleContainers(t *testing.T) {
	t.Parallel()

	docker := &spawnerTestDockerClient{list: mobyclient.ContainerListResult{
		Items: []container.Summary{
			{ID: "kept", State: "running"},
			{ID: "running", State: "running"},
			{ID: "created", State: reapStateCreated},
			{ID: "exited", State: "exited"},
			{ID: "removing", State: "removing"},
		},
	}}
	spawner := newSpawnerTestSubject(docker)

	reaped, err := spawner.ReapOrphans(
		context.Background(),
		map[string]struct{}{"kept": {}},
	)
	require.NoError(t, err)
	assert.Equal(t, 3, reaped)
	assert.ElementsMatch(t, []string{"running", "created", "exited"}, docker.stopped)
	assert.ElementsMatch(t, []string{"running", "created", "exited"}, docker.removed)
	assert.True(t, shouldReapState("running"))
	assert.False(t, shouldReapState("removing"))
	assert.Equal(t, "de_berlin", sanitize("de.berlin"))
	assert.Equal(t, "DE", exitCountryFromConf("de-berlin"))
	assert.Empty(t, exitCountryFromConf("provider"))
	require.Len(t, docker.listOptions, 1)
	assert.Equal(
		t,
		map[string]bool{
			LabelManaged + "=true":     true,
			LabelScope + "=test-scope": true,
		},
		docker.listOptions[0].Filters["label"],
	)
}

func newSpawnerTestSubject(docker DockerClient) *CellSpawner {
	return &CellSpawner{
		cfg: Config{
			CellImage:     "psyb0t/pr0xteus:cell-v0.1.0@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			CellSocksPort: 1080,
			ManagedScope:  "test-scope",
		},
		docker: docker,
		nowFn:  time.Now,
	}
}
