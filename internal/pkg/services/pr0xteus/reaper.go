package pr0xteus

import (
	"context"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/psyb0t/ctxscope"
)

// Reaper runs the two background loops that keep tunnel state
// honest:
//
//   - the IDLE loop kills tunnels that have been untouched for
//     IdleTimeout AND have zero in-flight requests
//   - the HEALTH loop probes each hot tunnel's the cell's SOCKS5 listener
//     port; tunnels whose handshake aged out get marked unhealthy
//     so the next POST /v1/proxies request re-spawns
//
// Both loops respect ctx cancellation + drain cleanly on Shutdown.
type Reaper struct {
	cfg     Config
	mgr     *Manager
	spawner Spawner
	http    HTTPDoer

	wg sync.WaitGroup

	nowFn func() time.Time
}

// defaultHealthProbeTimeout caps the per-tunnel health probe so
// one stuck tunnel can't block the whole reap loop.
const defaultHealthProbeTimeout = 3 * time.Second

// killCtxTimeout bounds docker stop+remove RPCs so a wedged daemon
// doesn't stall the reaper indefinitely.
const killCtxTimeout = 15 * time.Second

// NewReaper constructs a Reaper. The doer arg is used by the health
// loop to probe the cell's SOCKS5 port; pass nil to use a default
// http.Client with a short timeout.
func NewReaper(
	cfg Config, mgr *Manager, spawner Spawner, doer HTTPDoer,
) *Reaper {
	if doer == nil {
		doer = &http.Client{Timeout: defaultHealthProbeTimeout}
	}

	return &Reaper{
		cfg:     cfg,
		mgr:     mgr,
		spawner: spawner,
		http:    doer,
		nowFn:   time.Now,
	}
}

// Start launches the background loops. Idempotent: a second call
// is a no-op. Returns immediately; the loops run until ctx is
// cancelled.
func (r *Reaper) Start(ctx context.Context) {
	logger := ctxscope.GetLogger(ctx)
	logger.Info(
		"starting tunnel reaper + health monitor",
		"idle_timeout", r.cfg.IdleTimeout,
		"health_interval", r.cfg.HealthCheckInterval,
	)

	r.wg.Add(3) //nolint:mnd // 3 goroutines launched below

	go r.idleLoop(ctx)
	go r.healthLoop(ctx)
	go r.orphanLoop(ctx)
}

// orphanReapInterval is how often the orphan reconciler scans the
// docker daemon for managed cells that drifted out of in-memory
// pool state. One minute is short enough that a failed-spawn
// cleanup window doesn't leak meaningful resources, long enough
// that the scan cost is negligible.
const orphanReapInterval = time.Minute

// orphanLoop periodically reconciles docker against the pool state:
// any container labelled pr0xteus.managed=true that is
// NOT a tracked hot tunnel gets killed. Catches the residue of
// failed spawns (Created state), of process restarts (no in-memory
// tracking), and of pool churn (replaced tunnels whose old container
// id is no longer in PoolState).
func (r *Reaper) orphanLoop(ctx context.Context) {
	defer r.wg.Done()

	ticker := time.NewTicker(orphanReapInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		r.reapOrphans(ctx)
	}
}

// reapOrphans does one orphan-reconcile pass. Exposed for tests.
func (r *Reaper) reapOrphans(ctx context.Context) {
	logger := ctxscope.GetLogger(ctx)

	gs, ok := r.spawner.(*CellSpawner)
	if !ok {
		return
	}

	keep := make(map[string]struct{}, len(r.mgr.Pools()))

	for _, state := range r.mgr.Pools() {
		t := state.Snapshot()
		if t == nil || t.ContainerID == "" {
			continue
		}

		keep[t.ContainerID] = struct{}{}
	}

	count, err := gs.ReapOrphans(ctx, keep)
	if err != nil {
		logger.Warn("orphan reap failed", "err", err)

		return
	}

	if count > 0 {
		logger.Info("orphan reaper removed cells", "count", count)
	}
}

// Shutdown blocks until both loops have exited. Call after the
// supervising ctx has been cancelled.
func (r *Reaper) Shutdown() { r.wg.Wait() }

// idleLoop ticks every minute and kills tunnels that have been
// untouched for IdleTimeout AND have zero in-flight requests.
func (r *Reaper) idleLoop(ctx context.Context) {
	defer r.wg.Done()

	const tickEvery = time.Minute

	ticker := time.NewTicker(tickEvery)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		r.reapIdle(ctx)
	}
}

// reapIdle walks every pool, considers each hot tunnel for reaping.
// Exposed for tests via TickIdle.
func (r *Reaper) reapIdle(ctx context.Context) {
	logger := ctxscope.GetLogger(ctx)
	now := r.nowFn()
	controlURLs := r.childControlURLs(ctx)

	for name, state := range r.mgr.Pools() {
		t := state.Snapshot()
		if t == nil {
			continue
		}

		idleFor := now.Sub(t.LastUsedAt)
		if idleFor < r.cfg.IdleTimeout {
			continue
		}

		if t.InFlight > 0 {
			logger.Debug(
				"tunnel idle but in-flight; deferring reap",
				"pool", name, "container", t.ContainerID,
				"in_flight", t.InFlight,
			)

			continue
		}

		if r.hasLiveConnections(ctx, t.ContainerID, controlURLs[t.ContainerID]) {
			logger.Debug(
				"tunnel idle but has live connections; deferring reap",
				"pool", name, "container", t.ContainerID,
			)

			continue
		}

		r.killTunnel(ctx, name, state, t, "idle")
	}
}

// healthLoop ticks every HealthCheckInterval and probes each hot
// tunnel via the cell's SOCKS5 port. Stale handshakes are marked
// unhealthy; the pool sets the tunnel state so the next POST /v1/proxies
// request triggers a re-spawn.
func (r *Reaper) healthLoop(ctx context.Context) {
	defer r.wg.Done()

	ticker := time.NewTicker(r.cfg.HealthCheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		r.checkHealth(ctx)
	}
}

// checkHealth is the per-tick body of the health loop.
func (r *Reaper) checkHealth(ctx context.Context) {
	logger := ctxscope.GetLogger(ctx)
	controlURLs := r.childControlURLs(ctx)

	for name, state := range r.mgr.Pools() {
		t := state.Snapshot()
		if t == nil || t.State != TunnelStateHot {
			continue
		}

		// Tunnel is hot. Probe the cell's real cellproxy /healthz; if it does not
		// answer (or the handshake aged out in smoke mode), mark unhealthy + kill.
		healthy := r.probeHealthy(ctx, t, controlURLs[t.ContainerID])
		if healthy {
			state.mu.Lock()

			cur := state.tunnel
			if cur != nil && cur.ContainerID == t.ContainerID {
				cur.HealthyAt = r.nowFn()
			}

			state.mu.Unlock()

			continue
		}

		logger.Warn(
			"tunnel reports unhealthy; reaping",
			"pool", name, "container", t.ContainerID,
			"conf", t.ConfName,
		)

		state.markFailed(t.ConfName, r.nowFn())
		r.killTunnel(ctx, name, state, t, "unhealthy")
	}
}

// childControlURLs discovers this controller's cells from docker and maps each
// container ID to its current cellproxy control URL. The health + idle loops
// call it once per tick so each cell's address is whatever docker reports right
// now, never a stored value that could go stale.
func (r *Reaper) childControlURLs(ctx context.Context) map[string]*url.URL {
	handles, err := r.spawner.ListChildren(ctx)
	if err != nil {
		ctxscope.GetLogger(ctx).Warn("listing cells for reap failed", "err", err)

		return nil
	}

	urls := make(map[string]*url.URL, len(handles))
	for _, handle := range handles {
		if handle.ControlURL != nil {
			urls[handle.ContainerID] = handle.ControlURL
		}
	}

	return urls
}

// probeHealthy reports whether a hot tunnel is still serving. With a control URL
// (cell-network mode) it hits the cell's cellproxy /healthz — a real liveness
// signal, not a timer. In host-loopback smoke mode the control server has no
// stable address, so it falls back to the handshake-age heuristic: a tunnel
// whose last confirmed-healthy timestamp aged past HealthHandshakeMaxAge is
// assumed to have rotated past its keepalive window and is respawned.
func (r *Reaper) probeHealthy(
	ctx context.Context, t *Tunnel, controlURL *url.URL,
) bool {
	if controlURL != nil {
		return cellControlClient{http: r.http}.Healthy(ctx, controlURL)
	}

	age := r.nowFn().Sub(t.HealthyAt)

	return age <= r.cfg.HealthHandshakeMaxAge
}

// hasLiveConnections reports whether the cell currently has active proxied
// connections, per its cellproxy /status. This makes idle-reap session-aware:
// a tunnel untouched by the allocator but still carrying live traffic is not
// killed underneath its callers. A nil control URL or failed status fetch
// returns false so a cell that has genuinely died can still be reaped.
func (r *Reaper) hasLiveConnections(
	ctx context.Context, containerID string, controlURL *url.URL,
) bool {
	if controlURL == nil {
		return false
	}

	status, err := cellControlClient{http: r.http}.Status(ctx, controlURL)
	if err != nil {
		ctxscope.GetLogger(ctx).Debug(
			"idle-reap status probe failed; treating as no live connections",
			"container", containerID, "err", err,
		)

		return false
	}

	return status.Traffic.Active > 0
}

// killTunnel marks the tunnel for reaping, fires docker stop, and
// clears the pool's hot slot once the spawner returns.
func (r *Reaper) killTunnel(
	ctx context.Context, pool string, state *PoolState, t *Tunnel,
	reason string,
) {
	logger := ctxscope.GetLogger(ctx)

	state.mu.Lock()

	if state.tunnel != nil && state.tunnel.ContainerID == t.ContainerID {
		state.tunnel.State = TunnelStateReaping
	}

	state.mu.Unlock()

	killCtx, cancel := context.WithTimeout(ctx, killCtxTimeout)
	defer cancel()

	if err := r.spawner.Kill(killCtx, t.ContainerID); err != nil {
		logger.Warn(
			"docker kill errored; clearing pool slot anyway",
			"pool", pool, "container", t.ContainerID,
			"reason", reason, "err", err,
		)
	}

	state.setTunnel(nil)

	logger.Info(
		"tunnel reaped",
		"pool", pool, "container", t.ContainerID,
		"conf", t.ConfName, "reason", reason,
	)
}
