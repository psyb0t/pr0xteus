package pr0xteus

import (
	"context"
	"testing"
	"time"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/pr0xteus/internal/pkg/cellproxy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newControlTestReaper(t *testing.T, cfg Config, doer HTTPDoer) *Reaper {
	t.Helper()

	manager := NewManager(
		cfg,
		map[string]PoolSpec{"western": {Name: "western"}},
		&Router{},
		&cellsTestSpawner{},
	)

	return NewReaper(cfg, manager, &cellsTestSpawner{}, doer)
}

func TestReaper_ProbeHealthyViaControl(t *testing.T) {
	t.Parallel()

	server := cellControlServer(t, cellproxy.Status{})
	reaper := newControlTestReaper(t, Config{}, server.Client())

	assert.True(t, reaper.probeHealthy(
		context.Background(), &Tunnel{}, mustParseURL(t, server.URL),
	))
	assert.False(t, reaper.probeHealthy(
		context.Background(), &Tunnel{}, mustParseURL(t, "http://127.0.0.1:1"),
	))
}

func TestReaper_ProbeHealthyFallsBackToHandshakeAge(t *testing.T) {
	t.Parallel()

	now := time.Now()
	cfg := Config{HealthHandshakeMaxAge: time.Minute}
	reaper := newControlTestReaper(t, cfg, nil)
	reaper.nowFn = func() time.Time { return now }

	fresh := &Tunnel{HealthyAt: now.Add(-30 * time.Second)}
	assert.True(t, reaper.probeHealthy(context.Background(), fresh, nil))

	stale := &Tunnel{HealthyAt: now.Add(-2 * time.Minute)}
	assert.False(t, reaper.probeHealthy(context.Background(), stale, nil))
}

func TestReaper_HasLiveConnections(t *testing.T) {
	t.Parallel()

	busy := cellControlServer(t, cellproxy.Status{
		Traffic: cellproxy.Stats{Active: 4},
	})
	idle := cellControlServer(t, cellproxy.Status{
		Traffic: cellproxy.Stats{Active: 0},
	})

	reaper := newControlTestReaper(t, Config{}, busy.Client())
	ctx := context.Background()

	assert.True(t, reaper.hasLiveConnections(ctx, "busy", mustParseURL(t, busy.URL)))
	assert.False(t, reaper.hasLiveConnections(ctx, "idle", mustParseURL(t, idle.URL)))
	assert.False(t, reaper.hasLiveConnections(ctx, "none", nil))
	assert.False(t, reaper.hasLiveConnections(
		ctx, "dead", mustParseURL(t, "http://127.0.0.1:1"),
	))
}

func TestReaper_ChildControlURLsHandlesDockerListError(t *testing.T) {
	t.Parallel()

	spawner := &cellsTestSpawner{listErr: ctxerrors.New("docker daemon down")}
	manager := NewManager(
		Config{},
		map[string]PoolSpec{"western": {Name: "western"}},
		&Router{},
		spawner,
	)
	reaper := NewReaper(Config{}, manager, spawner, nil)

	assert.Empty(t, reaper.childControlURLs(context.Background()))
}

func TestReaper_DefersIdleReapWhenCellHasLiveConnections(t *testing.T) {
	t.Parallel()

	busy := cellControlServer(t, cellproxy.Status{
		Traffic: cellproxy.Stats{Active: 2},
	})

	now := time.Unix(2_000_000_000, 0)
	spawner := &runtimeTestSpawner{
		children: []CellHandle{{
			ContainerID: "busy-cell",
			ControlURL:  mustParseURL(t, busy.URL),
		}},
	}
	manager := newRuntimeTestManager(spawner, map[string]PoolSpec{
		"idle": runtimePoolSpec("idle", "de-berlin"),
	}, map[string]string{"de": "idle"})
	manager.Pools()["idle"].setTunnel(&Tunnel{
		ContainerID: "busy-cell",
		ConfName:    "de-berlin",
		State:       TunnelStateHot,
		LastUsedAt:  now.Add(-2 * time.Minute),
		HealthyAt:   now,
	})

	reaper := NewReaper(
		Config{IdleTimeout: time.Minute, FailureCacheTTL: time.Minute},
		manager,
		spawner,
		busy.Client(),
	)
	reaper.nowFn = func() time.Time { return now }
	reaper.reapIdle(context.Background())

	assert.Empty(t, spawner.kills)
	require.NotNil(t, manager.Pools()["idle"].Snapshot())
}
