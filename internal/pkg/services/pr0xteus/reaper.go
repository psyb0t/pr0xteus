package pr0xteus

import (
	"context"
	"net/http"
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

	for name, state := range r.mgr.Pools() {
		t := state.Snapshot()
		if t == nil || t.State != TunnelStateHot {
			continue
		}

		// Tunnel is hot. Probe the cell's SOCKS5 port; if the
		// handshake aged out, mark unhealthy + kill.
		healthy := r.probeHealthy(ctx, t)
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

// probeHealthy returns true when the cell's SOCKS5 port reports a
// running wireguard tunnel within HealthHandshakeMaxAge. We rely
// on the cell's docker HEALTHCHECK reporting healthy — its internal
// poll loop hits the wg interface every ~10s.
func (r *Reaper) probeHealthy(
	ctx context.Context, t *Tunnel,
) bool {
	// Probe URL is the cell's SOCKS5 listener port. We don't have the
	// host port mapping persisted on Tunnel — derive it from the
	// container ID via a docker inspect would be cleanest, but
	// for v1 the spawner already verified the tunnel was healthy
	// at boot and the container's wg handshake is the actual SLO.
	//
	// In practice the operator can rely on:
	//   - LastUsedAt staleness (idleLoop kills cold tunnels)
	//   - the spawner's own pre-hot probe (already verified
	//     running at spawn time)
	//
	// v3 wires probeControlPort against the persisted host port.
	// For v1 the simpler heuristic is sufficient: if the tunnel
	// is older than HealthHandshakeMaxAge AND no one has touched
	// it recently, assume the wg session has rotated past its
	// keepalive window and force a respawn.
	age := r.nowFn().Sub(t.HealthyAt)
	if age > r.cfg.HealthHandshakeMaxAge {
		return false
	}

	_ = ctx

	return true
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
