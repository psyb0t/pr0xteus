package pr0xteus

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	mobyclient "github.com/moby/moby/client"
	"github.com/psyb0t/ctxerrors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type runtimeTestSpawner struct {
	mu sync.Mutex

	spawnRequests []SpawnRequest
	spawnErrors   map[string]error
	kills         []string
	spawnGate     <-chan struct{}
	children      []CellHandle
}

func (s *runtimeTestSpawner) ListChildren(_ context.Context) ([]CellHandle, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.children, nil
}

func (s *runtimeTestSpawner) Spawn(
	ctx context.Context, request SpawnRequest,
) (*Tunnel, error) {
	s.mu.Lock()
	s.spawnRequests = append(s.spawnRequests, request)
	spawnGate := s.spawnGate
	spawnErr := s.spawnErrors[request.Pool+":"+request.ConfName]
	s.mu.Unlock()

	if spawnGate != nil {
		select {
		case <-spawnGate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	if spawnErr != nil {
		return nil, spawnErr
	}

	return &Tunnel{
		ContainerID: "cell-" + request.Pool + "-" + request.ConfName,
		ConfName:    request.ConfName,
		ExitCountry: request.ExitCountry,
		ProxyURL: &url.URL{
			Scheme: "socks5",
			Host:   request.Pool + ".cell:1080",
		},
	}, nil
}

func (s *runtimeTestSpawner) Kill(_ context.Context, containerID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.kills = append(s.kills, containerID)

	return nil
}

func (s *runtimeTestSpawner) requests() []SpawnRequest {
	s.mu.Lock()
	defer s.mu.Unlock()

	return append([]SpawnRequest(nil), s.spawnRequests...)
}

func (s *runtimeTestSpawner) killed() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return append([]string(nil), s.kills...)
}

func TestManager_AcquiresAndReleasesHotTunnel(t *testing.T) {
	t.Parallel()

	spawner := &runtimeTestSpawner{}
	manager := newRuntimeTestManager(spawner, map[string]PoolSpec{
		"primary": runtimePoolSpec("primary", "de-berlin"),
	}, map[string]string{"de": "primary"})

	acquisition, err := manager.AcquireForCountry(
		context.Background(), "DE", nil, false,
	)
	require.NoError(t, err)
	require.NotNil(t, acquisition.Tunnel)
	assert.Equal(t, "primary", acquisition.Pool)
	assert.Equal(
		t,
		"socks5://primary.cell:1080",
		acquisition.Tunnel.ProxyURL.String(),
	)
	require.Len(t, spawner.requests(), 1)

	tunnel := manager.Pools()["primary"].Snapshot()
	require.NotNil(t, tunnel)
	assert.Equal(t, TunnelStateHot, tunnel.State)
	assert.Equal(t, 1, tunnel.InFlight)

	manager.Release(acquisition)
	tunnel = manager.Pools()["primary"].Snapshot()
	require.NotNil(t, tunnel)
	assert.Zero(t, tunnel.InFlight)
}

func TestManager_UsesOneSpawnForConcurrentSamePoolAcquisitions(t *testing.T) {
	t.Parallel()

	spawnGate := make(chan struct{})
	spawner := &runtimeTestSpawner{spawnGate: spawnGate}
	manager := newRuntimeTestManager(spawner, map[string]PoolSpec{
		"primary": runtimePoolSpec("primary", "de-berlin"),
	}, map[string]string{"de": "primary"})

	type result struct {
		acquisition Acquisition
		err         error
	}
	results := make(chan result, 2)

	for range 2 {
		go func() {
			acquisition, err := manager.AcquireForCountry(
				context.Background(), "DE", nil, false,
			)
			results <- result{acquisition: acquisition, err: err}
		}()
	}

	require.Eventually(t, func() bool {
		return len(spawner.requests()) == 1
	}, time.Second, 10*time.Millisecond)
	close(spawnGate)

	for range 2 {
		outcome := <-results
		require.NoError(t, outcome.err)
		require.NotNil(t, outcome.acquisition.Tunnel)
		manager.Release(outcome.acquisition)
	}

	assert.Len(t, spawner.requests(), 1)
}

// TestManager_SpawnedAcquisitionTunnelIsIndependentCopy guards the -race fix
// in spawnFromState: the Acquisition handed back to the caller must be a
// standalone copy, never the pool's shared *Tunnel, or a concurrent acquire
// mutating the pool's tunnel under its own mutex would race the caller's
// unsynchronized read of the same pointer.
func TestManager_SpawnedAcquisitionTunnelIsIndependentCopy(t *testing.T) {
	t.Parallel()

	spawner := &runtimeTestSpawner{}
	manager := newRuntimeTestManager(spawner, map[string]PoolSpec{
		"primary": runtimePoolSpec("primary", "de-berlin"),
	}, map[string]string{"de": "primary"})

	first, err := manager.AcquireForCountry(context.Background(), "DE", nil, false)
	require.NoError(t, err)
	require.NotNil(t, first.Tunnel)
	assert.Equal(t, 1, first.Tunnel.InFlight)

	state := manager.Pools()["primary"]
	state.mu.Lock()
	poolTunnelPtr := state.tunnel
	state.mu.Unlock()
	require.NotNil(t, poolTunnelPtr)
	assert.NotSame(t, poolTunnelPtr, first.Tunnel)

	second, err := manager.AcquireForCountry(context.Background(), "DE", nil, false)
	require.NoError(t, err)
	require.NotNil(t, second.Tunnel)

	assert.Equal(
		t, 1, first.Tunnel.InFlight,
		"the first caller's copy must not observe a later in-flight bump",
	)
	assert.Equal(t, 2, second.Tunnel.InFlight)
}

// TestManager_ConcurrentAcquisitionsReturnIndependentTunnelCopies exercises
// the same guarantee under actual goroutine concurrency + -race: each
// caller mutates only its own returned copy.
func TestManager_ConcurrentAcquisitionsReturnIndependentTunnelCopies(t *testing.T) {
	t.Parallel()

	spawnGate := make(chan struct{})
	spawner := &runtimeTestSpawner{spawnGate: spawnGate}
	manager := newRuntimeTestManager(spawner, map[string]PoolSpec{
		"primary": runtimePoolSpec("primary", "de-berlin"),
	}, map[string]string{"de": "primary"})

	type result struct {
		acquisition Acquisition
		err         error
	}
	results := make(chan result, 2)

	for range 2 {
		go func() {
			acquisition, err := manager.AcquireForCountry(
				context.Background(), "DE", nil, false,
			)
			results <- result{acquisition: acquisition, err: err}
		}()
	}

	require.Eventually(t, func() bool {
		return len(spawner.requests()) == 1
	}, time.Second, 10*time.Millisecond)
	close(spawnGate)

	for range 2 {
		outcome := <-results
		require.NoError(t, outcome.err)
		require.NotNil(t, outcome.acquisition.Tunnel)

		// Mutating the caller-local copy directly must never race the pool's
		// own concurrent writes to its shared tunnel.
		outcome.acquisition.Tunnel.InFlight = -1
		manager.Release(outcome.acquisition)
	}
}

func TestManager_UsesConfiguredFallbackAfterPrimarySpawnFailure(t *testing.T) {
	t.Parallel()

	spawner := &runtimeTestSpawner{
		spawnErrors: map[string]error{
			"primary:de-berlin": ctxerrors.New("provider rejected config"),
		},
	}
	manager := newRuntimeTestManager(spawner, map[string]PoolSpec{
		"primary": {
			Name:         "primary",
			Configs:      []string{"de-berlin"},
			FallbackPool: "fallback",
		},
		"fallback": runtimePoolSpec("fallback", "nl-amsterdam"),
	}, map[string]string{"de": "primary"})

	acquisition, err := manager.AcquireForCountry(
		context.Background(), "DE", nil, true,
	)
	require.NoError(t, err)
	assert.Equal(t, "fallback", acquisition.Pool)
	assert.Equal(
		t,
		[]SpawnRequest{
			{Pool: "primary", ConfName: "de-berlin"},
			{Pool: "fallback", ConfName: "nl-amsterdam"},
		},
		stripRuntimeSpawnRequestPaths(spawner.requests()),
	)
	assert.Equal(
		t,
		1,
		manager.Pools()["primary"].RecentlyFailedCount(
			time.Now(), time.Minute,
		),
	)
}

func TestManager_DoesNotEscalateWithoutFallbackPermission(t *testing.T) {
	t.Parallel()

	spawner := &runtimeTestSpawner{
		spawnErrors: map[string]error{
			"primary:de-berlin": ctxerrors.New("provider rejected config"),
		},
	}
	manager := newRuntimeTestManager(spawner, map[string]PoolSpec{
		"primary": {
			Name:         "primary",
			Configs:      []string{"de-berlin"},
			FallbackPool: "fallback",
		},
		"fallback": runtimePoolSpec("fallback", "nl-amsterdam"),
	}, map[string]string{"de": "primary"})

	acquisition, err := manager.AcquireForCountry(
		context.Background(), "DE", nil, false,
	)
	require.ErrorIs(t, err, ErrPoolUnavailable)
	assert.Empty(t, acquisition.Pool)
	assert.Equal(t, []SpawnRequest{{
		Pool:     "primary",
		ConfName: "de-berlin",
	}}, stripRuntimeSpawnRequestPaths(spawner.requests()))
}

func TestPoolState_ExpiresFailureCacheAndRespectsExcludedTunnel(t *testing.T) {
	t.Parallel()

	state := NewPoolState(runtimePoolSpec("primary", "de-berlin"))
	now := time.Unix(2_000_000_000, 0)
	state.markFailed("de-berlin", now)

	_, err := state.pickConf(now, nil, time.Minute)
	require.ErrorIs(t, err, ErrPoolExhausted)

	conf, err := state.pickConf(
		now.Add(time.Minute+time.Nanosecond), nil, time.Minute,
	)
	require.NoError(t, err)
	assert.Equal(t, "de-berlin", conf)

	excluded, err := url.Parse("socks5://primary.cell:1080")
	require.NoError(t, err)
	state.setTunnel(&Tunnel{
		ConfName: "de-berlin",
		ProxyURL: excluded,
		State:    TunnelStateHot,
	})
	_, err = state.acquire(excluded)
	require.ErrorIs(t, err, ErrPoolExhausted)
	assert.Equal(
		t,
		map[string]struct{}{"de-berlin": {}},
		excludeConfsFor(excluded, state),
	)
}

func TestManager_ViewsAreSortedAndProjectTunnelState(t *testing.T) {
	t.Parallel()

	spawner := &runtimeTestSpawner{}
	manager := newRuntimeTestManager(spawner, map[string]PoolSpec{
		"zulu":  runtimePoolSpec("zulu", "de-berlin"),
		"alpha": runtimePoolSpec("alpha", "nl-amsterdam"),
	}, map[string]string{"de": "zulu"})
	manager.Pools()["zulu"].setTunnel(&Tunnel{
		ConfName:    "de-berlin",
		ProxyURL:    &url.URL{Scheme: "socks5", Host: "zulu.cell:1080"},
		State:       TunnelStateHot,
		ExitCountry: "DE",
		LastUsedAt:  time.Now().Add(-time.Second),
	})

	views := manager.Views()
	require.Len(t, views, 2)
	assert.Equal(
		t,
		[]string{"alpha", "zulu"},
		[]string{views[0].Name, views[1].Name},
	)
	assert.Nil(t, views[0].Tunnel)
	require.NotNil(t, views[1].Tunnel)
	assert.Equal(t, "socks5://zulu.cell:1080", views[1].Tunnel.ProxyURL)
	assert.GreaterOrEqual(t, views[1].Tunnel.IdleSeconds, float64(1))
}

func TestPoolAndRouterFiles_LoadAndValidate(t *testing.T) {
	t.Parallel()

	t.Run("loads normalized metadata and routing", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		bundleDir := filepath.Join(dir, "wireguard")
		require.NoError(t, os.Mkdir(bundleDir, 0o700))
		writeRuntimeFixture(
			t,
			filepath.Join(bundleDir, "de-berlin.conf"),
			"[Interface]\n",
		)
		writeRuntimeFixture(
			t,
			filepath.Join(bundleDir, "nl-amsterdam.conf"),
			"[Interface]\n",
		)
		poolsPath := filepath.Join(dir, "pools.yaml")
		writeRuntimeFixture(t, poolsPath, `pools:
  western:
    region: eu
    configs: [nl-amsterdam, de-berlin]
    exit_countries:
      de-berlin: de
      nl-amsterdam: NL
`)

		specs, known, err := LoadPoolSpecs(poolsPath, bundleDir)
		require.NoError(t, err)
		assert.Equal(
			t,
			[]string{"de-berlin", "nl-amsterdam"},
			specs["western"].Configs,
		)
		assert.Equal(
			t,
			map[string]string{"de-berlin": "DE", "nl-amsterdam": "NL"},
			specs["western"].ExitCountries,
		)

		routingPath := filepath.Join(dir, "routing.yaml")
		writeRuntimeFixture(t, routingPath, `country_to_pool:
  DE: western
default_pool: western
`)
		router, err := LoadRouter(routingPath, "", known)
		require.NoError(t, err)
		pool, err := router.Resolve(" de ")
		require.NoError(t, err)
		assert.Equal(t, "western", pool)
		assert.Equal(t, []string{"de"}, router.Countries())
	})

	t.Run("rejects missing configs and unmapped countries", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		bundleDir := filepath.Join(dir, "wireguard")
		require.NoError(t, os.Mkdir(bundleDir, 0o700))
		poolsPath := filepath.Join(dir, "pools.yaml")
		writeRuntimeFixture(t, poolsPath, `pools:
  primary:
    configs: [missing]
`)
		_, _, err := LoadPoolSpecs(poolsPath, bundleDir)
		require.ErrorIs(t, err, ErrBundleConfMissing)

		routingPath := filepath.Join(dir, "routing.yaml")
		writeRuntimeFixture(t, routingPath, "country_to_pool: {}\n")
		router, err := LoadRouter(
			routingPath,
			"",
			map[string]struct{}{"primary": {}},
		)
		require.NoError(t, err)
		_, err = router.Resolve("DE")
		require.ErrorIs(t, err, ErrInvalidCountry)
	})
}

func TestPoolAndRouterFiles_RejectInvalidTopology(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name      string
		poolsFile string
	}{
		{name: "no pools", poolsFile: "pools: {}\n"},
		{
			name: "pool without configs",
			poolsFile: `pools:
  primary:
    configs: []
`,
		},
		{
			name: "exit country names another config",
			poolsFile: `pools:
  primary:
    configs: [primary]
    exit_countries:
      another: DE
`,
		},
		{
			name: "exit country is not alpha two",
			poolsFile: `pools:
  primary:
    configs: [primary]
    exit_countries:
      primary: D
`,
		},
		{
			name: "fallback is not defined",
			poolsFile: `pools:
  primary:
    configs: [primary]
    fallback_pool: missing
`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			bundleDir := filepath.Join(dir, "wireguard")
			require.NoError(t, os.Mkdir(bundleDir, 0o700))
			writeRuntimeFixture(
				t,
				filepath.Join(bundleDir, "primary.conf"),
				"[Interface]\n",
			)

			poolsPath := filepath.Join(dir, "pools.yaml")
			writeRuntimeFixture(t, poolsPath, tc.poolsFile)
			_, _, err := LoadPoolSpecs(poolsPath, bundleDir)
			require.ErrorIs(t, err, ErrConfigInvalid)
		})
	}

	knownPools := map[string]struct{}{"primary": {}}
	routerTestCases := []struct {
		name     string
		contents string
	}{
		{name: "malformed yaml", contents: "country_to_pool: [\n"},
		{
			name: "country maps to unknown pool",
			contents: `country_to_pool:
  DE: missing
`,
		},
		{name: "default is unknown pool", contents: "default_pool: missing\n"},
	}

	for _, tc := range routerTestCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			routingPath := filepath.Join(t.TempDir(), "routing.yaml")
			writeRuntimeFixture(t, routingPath, tc.contents)
			_, err := LoadRouter(routingPath, "", knownPools)
			require.ErrorIs(t, err, ErrConfigInvalid)
		})
	}
}

func TestManager_MarksTunnelsFailedAndExposesDiagnostics(t *testing.T) {
	t.Parallel()

	spawner := &runtimeTestSpawner{}
	manager := newRuntimeTestManager(spawner, map[string]PoolSpec{
		"primary": runtimePoolSpec("primary", "de-berlin"),
	}, map[string]string{"de": "primary"})
	manager.Pools()["primary"].setTunnel(&Tunnel{
		ConfName: "de-berlin",
		State:    TunnelStateHot,
	})

	manager.MarkFailed("primary", "de-berlin")
	manager.MarkFailed("missing", "de-berlin")
	manager.Release(Acquisition{Pool: "missing"})
	manager.Release(Acquisition{Pool: "primary"})

	tunnel := manager.Pools()["primary"].Snapshot()
	require.NotNil(t, tunnel)
	assert.Equal(t, TunnelStateUnhealthy, tunnel.State)
	assert.Equal(
		t,
		1,
		manager.Pools()["primary"].RecentlyFailedCount(time.Now(), time.Minute),
	)
	assert.Equal(t, "/test-bundle", manager.PoolsDir())
	assert.Empty(t, manager.router.DefaultPool())
}

func TestManager_RejectsUnknownRoutingAndClosesOnlyTrackedCells(t *testing.T) {
	t.Parallel()

	spawner := &runtimeTestSpawner{}
	manager := newRuntimeTestManager(spawner, map[string]PoolSpec{
		"primary": runtimePoolSpec("primary", "de-berlin"),
		"cold":    runtimePoolSpec("cold", "nl-amsterdam"),
	}, map[string]string{})
	manager.Pools()["primary"].setTunnel(&Tunnel{
		ContainerID: "primary-cell",
		ConfName:    "de-berlin",
		State:       TunnelStateHot,
	})

	_, err := manager.AcquireForCountry(context.Background(), "DE", nil, false)
	require.ErrorIs(t, err, ErrInvalidCountry)
	_, err = manager.AcquireForPool(context.Background(), "missing", nil, false)
	require.ErrorIs(t, err, ErrUnknownPool)

	manager.Close(context.Background())
	assert.Equal(t, []string{"primary-cell"}, spawner.killed())
	assert.Nil(t, manager.Pools()["primary"].Snapshot())
	assert.Nil(t, manager.Pools()["cold"].Snapshot())
}

func TestReaper_ReapsOnlyEligibleTunnels(t *testing.T) {
	t.Parallel()

	now := time.Unix(2_000_000_000, 0)
	spawner := &runtimeTestSpawner{}
	manager := newRuntimeTestManager(spawner, map[string]PoolSpec{
		"idle":   runtimePoolSpec("idle", "de-berlin"),
		"active": runtimePoolSpec("active", "nl-amsterdam"),
		"stale":  runtimePoolSpec("stale", "pl-warsaw"),
	}, map[string]string{"de": "idle"})
	manager.Pools()["idle"].setTunnel(&Tunnel{
		ContainerID: "idle-cell",
		ConfName:    "de-berlin",
		State:       TunnelStateHot,
		LastUsedAt:  now.Add(-2 * time.Minute),
		HealthyAt:   now,
	})
	manager.Pools()["active"].setTunnel(&Tunnel{
		ContainerID: "active-cell",
		ConfName:    "nl-amsterdam",
		State:       TunnelStateHot,
		InFlight:    1,
		LastUsedAt:  now.Add(-2 * time.Minute),
		HealthyAt:   now,
	})
	manager.Pools()["stale"].setTunnel(&Tunnel{
		ContainerID: "stale-cell",
		ConfName:    "pl-warsaw",
		State:       TunnelStateHot,
		LastUsedAt:  now,
		HealthyAt:   now.Add(-2 * time.Minute),
	})

	reaper := NewReaper(
		Config{
			IdleTimeout:           time.Minute,
			HealthHandshakeMaxAge: time.Minute,
			FailureCacheTTL:       time.Minute,
		},
		manager,
		spawner,
		nil,
	)
	reaper.nowFn = func() time.Time { return now }
	reaper.reapIdle(context.Background())
	reaper.checkHealth(context.Background())

	assert.ElementsMatch(t, []string{"idle-cell", "stale-cell"}, spawner.killed())
	assert.Nil(t, manager.Pools()["idle"].Snapshot())
	require.NotNil(t, manager.Pools()["active"].Snapshot())
	assert.Nil(t, manager.Pools()["stale"].Snapshot())
	assert.Equal(
		t,
		1,
		manager.Pools()["stale"].RecentlyFailedCount(now, time.Minute),
	)
}

func TestReaper_ReconcilesOnlyUntrackedCells(t *testing.T) {
	t.Parallel()

	docker := &spawnerTestDockerClient{list: mobyclient.ContainerListResult{
		Items: []container.Summary{
			{ID: "tracked-cell", State: "running"},
			{ID: "orphan-cell", State: "running"},
			{ID: "already-removing", State: "removing"},
		},
	}}
	spawner := newSpawnerTestSubject(docker)
	manager := newRuntimeTestManager(spawner, map[string]PoolSpec{
		"primary": runtimePoolSpec("primary", "de-berlin"),
	}, map[string]string{"de": "primary"})
	manager.Pools()["primary"].setTunnel(&Tunnel{
		ContainerID: "tracked-cell",
		State:       TunnelStateHot,
	})

	reaper := NewReaper(Config{}, manager, spawner, nil)
	reaper.reapOrphans(context.Background())

	assert.Equal(t, []string{"orphan-cell"}, docker.stopped)
	assert.Equal(t, []string{"orphan-cell"}, docker.removed)
}

func newRuntimeTestManager(
	spawner Spawner,
	specs map[string]PoolSpec,
	countries map[string]string,
) *Manager {
	return NewManager(
		Config{
			BundleDir:       "/test-bundle",
			FailureCacheTTL: time.Minute,
			SpawnTimeout:    time.Second,
		},
		specs,
		&Router{countryToPool: countries},
		spawner,
	)
}

func runtimePoolSpec(name, conf string) PoolSpec {
	return PoolSpec{
		Name:          name,
		Configs:       []string{conf},
		ExitCountries: map[string]string{},
	}
}

func stripRuntimeSpawnRequestPaths(
	requests []SpawnRequest,
) []SpawnRequest {
	for index := range requests {
		requests[index].BundleDir = ""
	}

	return requests
}

func writeRuntimeFixture(t *testing.T, path, contents string) {
	t.Helper()
	require.NoError(t, os.WriteFile(path, []byte(contents), 0o600))
}
