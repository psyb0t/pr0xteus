package pr0xteus

import (
	"context"
	"errors"
	"math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxscope"
	"gopkg.in/yaml.v3"
)

// poolsFile mirrors the on-disk shape of pools.yaml.
type poolsFile struct {
	Pools map[string]poolEntry `yaml:"pools"`
}

// poolEntry mirrors one pool YAML block. snake_case is the operator-
// facing YAML convention; tagliatelle prefers camelCase but the
// existing files would break.
//
//nolint:tagliatelle // operator-managed YAML uses snake_case
type poolEntry struct {
	Region        string            `yaml:"region"`
	Purpose       string            `yaml:"purpose"`
	Configs       []string          `yaml:"configs"`
	ExitCountries map[string]string `yaml:"exit_countries,omitempty"`
	FallbackPool  string            `yaml:"fallback_pool,omitempty"`
}

// PoolSpec is the parsed + validated definition of one logical pool.
// Built at startup from pools.yaml; immutable thereafter.
type PoolSpec struct {
	Name          string
	Region        string
	Purpose       string
	Configs       []string // sorted .conf basenames (no extension)
	ExitCountries map[string]string
	FallbackPool  string // optional; empty when no fallback
}

// LoadPoolSpecs reads pools.yaml + verifies every referenced .conf
// exists under bundleDir. Returns the per-pool specs keyed by pool
// name plus a known-pools set the router uses for cross-validation.
func LoadPoolSpecs(
	path, bundleDir string,
) (map[string]PoolSpec, map[string]struct{}, error) {
	parsed, err := readPoolsFile(path)
	if err != nil {
		return nil, nil, err
	}

	bundle, err := readBundle(bundleDir)
	if err != nil {
		return nil, nil, err
	}

	specs, known, err := buildSpecs(parsed, bundle, bundleDir)
	if err != nil {
		return nil, nil, err
	}

	if err := validateFallbacks(specs); err != nil {
		return nil, nil, err
	}

	return specs, known, nil
}

// readPoolsFile parses pools.yaml + enforces the file is non-empty.
func readPoolsFile(path string) (poolsFile, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return poolsFile{}, ctxerrors.Wrapf(
			ErrConfigInvalid, "read %s: %s", path, err.Error(),
		)
	}

	var parsed poolsFile
	if err := yaml.Unmarshal(raw, &parsed); err != nil {
		return poolsFile{}, ctxerrors.Wrapf(
			ErrConfigInvalid, "yaml unmarshal %s: %s", path, err.Error(),
		)
	}

	if len(parsed.Pools) == 0 {
		return poolsFile{}, ctxerrors.Wrap(
			ErrConfigInvalid, "pools.yaml has no pools",
		)
	}

	return parsed, nil
}

// buildSpecs validates each pool's configs against the bundle and
// returns the typed spec map + known-set.
func buildSpecs(
	parsed poolsFile, bundle map[string]struct{}, bundleDir string,
) (map[string]PoolSpec, map[string]struct{}, error) {
	specs := make(map[string]PoolSpec, len(parsed.Pools))
	known := make(map[string]struct{}, len(parsed.Pools))

	for name, entry := range parsed.Pools {
		if len(entry.Configs) == 0 {
			return nil, nil, ctxerrors.Wrapf(
				ErrConfigInvalid,
				"pool %q has no configs", name,
			)
		}

		for _, conf := range entry.Configs {
			if _, ok := bundle[conf]; !ok {
				return nil, nil, ctxerrors.Wrapf(
					ErrBundleConfMissing,
					"pool %q references %q, not in %s",
					name, conf, bundleDir,
				)
			}
		}

		configs := slices.Clone(entry.Configs)
		sort.Strings(configs)

		exitCountries, err := normalizeExitCountries(
			configs, entry.ExitCountries,
		)
		if err != nil {
			return nil, nil, err
		}

		specs[name] = PoolSpec{
			Name:          name,
			Region:        entry.Region,
			Purpose:       entry.Purpose,
			Configs:       configs,
			ExitCountries: exitCountries,
			FallbackPool:  entry.FallbackPool,
		}
		known[name] = struct{}{}
	}

	return specs, known, nil
}

// normalizeExitCountries validates optional per-config metadata. Omitting the
// map retains filename-derived country support, while the explicit form makes
// arbitrary provider filenames unambiguous.
func normalizeExitCountries(
	configs []string, exitCountries map[string]string,
) (map[string]string, error) {
	if len(exitCountries) == 0 {
		return map[string]string{}, nil
	}

	knownConfigs := make(map[string]struct{}, len(configs))
	for _, configName := range configs {
		knownConfigs[configName] = struct{}{}
	}

	normalized := make(map[string]string, len(exitCountries))
	for configName, exitCountry := range exitCountries {
		if _, ok := knownConfigs[configName]; !ok {
			return nil, ctxerrors.Wrapf(
				ErrConfigInvalid,
				"exit_countries names config %q outside its pool",
				configName,
			)
		}

		country := strings.ToUpper(strings.TrimSpace(exitCountry))
		if !isCountryCode(country) {
			return nil, ctxerrors.Wrapf(
				ErrConfigInvalid,
				"exit_countries[%q] must be an ISO 3166-1 alpha-2 code",
				configName,
			)
		}

		normalized[configName] = country
	}

	return normalized, nil
}

// validateFallbacks ensures every fallback_pool reference resolves
// to a known pool. Errors at boot rather than at retry-attempt-5.
func validateFallbacks(specs map[string]PoolSpec) error {
	for name, spec := range specs {
		if spec.FallbackPool == "" {
			continue
		}

		if _, ok := specs[spec.FallbackPool]; !ok {
			return ctxerrors.Wrapf(
				ErrConfigInvalid,
				"pool %q fallback %q not defined",
				name, spec.FallbackPool,
			)
		}
	}

	return nil
}

// readBundle scans bundleDir for *.conf files and returns the set
// of basenames (extension stripped) so callers can membership-test
// in O(1).
func readBundle(bundleDir string) (map[string]struct{}, error) {
	entries, err := os.ReadDir(bundleDir)
	if err != nil {
		return nil, ctxerrors.Wrapf(
			ErrConfigInvalid,
			"read bundle dir %s: %s", bundleDir, err.Error(),
		)
	}

	out := make(map[string]struct{}, len(entries))

	for _, e := range entries {
		if e.IsDir() {
			continue
		}

		name := e.Name()
		if !strings.HasSuffix(name, ".conf") {
			continue
		}

		out[strings.TrimSuffix(name, ".conf")] = struct{}{}
	}

	return out, nil
}

// PoolState is the runtime state for one pool. Holds the (optional)
// hot tunnel, the recently-failed cache, and a mutex guarding both.
// One PoolState per pool name; a Manager owns the map.
type PoolState struct {
	Spec PoolSpec

	mu sync.Mutex

	tunnel *Tunnel

	// failed records .conf basenames that recently failed to spawn
	// or stay healthy. Entries TTL out via cleanFailedLocked.
	failed map[string]time.Time
}

// NewPoolState constructs an idle PoolState for the given spec.
func NewPoolState(spec PoolSpec) *PoolState {
	return &PoolState{
		Spec:   spec,
		failed: make(map[string]time.Time),
	}
}

// Snapshot returns a pointer-safe copy of the current tunnel, or
// nil when the pool is cold. Callers MUST NOT mutate the returned
// pointer; it's a copy intended for read-only inspection.
func (p *PoolState) Snapshot() *Tunnel {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.tunnel == nil {
		return nil
	}

	tunnel := *p.tunnel

	return &tunnel
}

// pickConf selects a .conf basename for a fresh spawn, skipping
// the recently-failed cache and the optional exclude set (used by
// retry attempts so the pool doesn't re-pick a .conf the caller
// just gave up on). Returns ErrPoolExhausted when no candidate
// remains.
func (p *PoolState) pickConf(
	now time.Time, exclude map[string]struct{}, ttl time.Duration,
) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.cleanFailedLocked(now, ttl)

	candidates := make([]string, 0, len(p.Spec.Configs))

	for _, conf := range p.Spec.Configs {
		if _, banned := p.failed[conf]; banned {
			continue
		}

		if _, excluded := exclude[conf]; excluded {
			continue
		}

		candidates = append(candidates, conf)
	}

	if len(candidates) == 0 {
		return "", ctxerrors.Wrapf(
			ErrPoolExhausted,
			"pool %q: no candidate (failed=%d, excluded=%d)",
			p.Spec.Name, len(p.failed), len(exclude),
		)
	}

	//nolint:gosec // .conf rotation is decoration, no crypto entropy needed
	return candidates[rand.IntN(len(candidates))], nil
}

// markFailed records a failure for the given .conf. The entry
// stays in the failed cache for ttl, after which subsequent
// pickConf calls may consider it again.
func (p *PoolState) markFailed(conf string, now time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.failed[conf] = now
}

// cleanFailedLocked drops expired entries from the failed cache.
// Caller MUST hold p.mu.
func (p *PoolState) cleanFailedLocked(now time.Time, ttl time.Duration) {
	for conf, at := range p.failed {
		if now.Sub(at) > ttl {
			delete(p.failed, conf)
		}
	}
}

// RecentlyFailedCount returns the size of the failed cache after
// trimming expired entries. Test + admin-view helper.
func (p *PoolState) RecentlyFailedCount(
	now time.Time, ttl time.Duration,
) int {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.cleanFailedLocked(now, ttl)

	return len(p.failed)
}

// setTunnel atomically swaps in the given tunnel as the pool's
// hot tunnel. Used by the spawner once a fresh container reaches
// hot state, and by the reaper to clear (setTunnel(nil)) before
// killing the container.
func (p *PoolState) setTunnel(t *Tunnel) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.tunnel = t

	hot := 0.0
	if t != nil {
		hot = 1
	}

	TunnelHotGauge.WithLabelValues(p.Spec.Name).Set(hot)
}

// acquire returns the hot tunnel and atomically increments its
// in-flight count, so the reaper won't kill it mid-request.
// Returns nil + ErrPoolUnavailable when the pool has no hot tunnel.
//
// The excludeProxy parameter, when non-nil, makes acquire skip a
// tunnel whose ProxyURL matches — used by retry attempts to force
// rotation when the same pool is asked twice with the same proxy
// having just failed.
func (p *PoolState) acquire(excludeProxy *url.URL) (*Tunnel, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.tunnel == nil {
		return nil, ctxerrors.Wrapf(
			ErrPoolUnavailable,
			"pool %q has no hot tunnel", p.Spec.Name,
		)
	}

	if p.tunnel.State != TunnelStateHot {
		return nil, ctxerrors.Wrapf(
			ErrPoolUnavailable,
			"pool %q tunnel is %s", p.Spec.Name, p.tunnel.State,
		)
	}

	if excludeProxy != nil && p.tunnel.ProxyURL != nil &&
		sameURL(p.tunnel.ProxyURL, excludeProxy) {
		return nil, ctxerrors.Wrapf(
			ErrPoolExhausted,
			"pool %q only has the excluded proxy", p.Spec.Name,
		)
	}

	p.tunnel.InFlight++
	p.tunnel.LastUsedAt = time.Now()

	tunnel := *p.tunnel

	return &tunnel, nil
}

// release decrements the in-flight counter after a proxy-assignment
// HTTP exchange completes. Reaper uses this counter
// to refuse mid-flight kills.
func (p *PoolState) release() {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.tunnel == nil || p.tunnel.InFlight <= 0 {
		return
	}

	p.tunnel.InFlight--
}

// acquireContainer reserves the exact hot cell recorded in a proxy lease. A
// lease never silently switches to a newly spawned exit after its cell is gone.
func (p *PoolState) acquireContainer(containerID string) (*Tunnel, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.tunnel == nil || p.tunnel.State != TunnelStateHot ||
		p.tunnel.ContainerID != containerID {
		return nil, ctxerrors.Wrapf(
			ErrPoolUnavailable, "pool %q leased tunnel is unavailable", p.Spec.Name,
		)
	}

	p.tunnel.InFlight++
	p.tunnel.LastUsedAt = time.Now()
	tunnel := *p.tunnel

	return &tunnel, nil
}

func (p *PoolState) setLastURL(containerID, value string, expiresAt time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.tunnel == nil || p.tunnel.ContainerID != containerID {
		return
	}

	p.tunnel.LastURL = value
	p.tunnel.LastURLExpiresAt = expiresAt
}

// sameURL compares two *url.URL by scheme+host. Path is irrelevant
// for proxy comparison.
func sameURL(a, b *url.URL) bool {
	return a.Scheme == b.Scheme && a.Host == b.Host
}

// Manager owns every PoolState + serializes spawn requests. v1
// guarantees at most one in-flight spawn per pool to keep the
// docker socket from being flooded; v3 may grow N tunnels per pool
// + relax this.
type Manager struct {
	cfg     Config
	router  *Router
	specs   map[string]PoolSpec
	pools   map[string]*PoolState
	spawner Spawner

	// control scrapes each cell's cellproxy /status + /healthz when the API
	// lists cells or a caller inspects one.
	control cellControlClient
	leases  *leaseRegistry

	// spawnMu serializes per-pool spawn requests. Map of pool name
	// → mutex so two countries pointing at the same pool don't
	// race-spawn two containers for the same pool.
	spawnMuMu sync.Mutex
	spawnMu   map[string]*sync.Mutex
}

// cellControlTimeout bounds a single control-plane scrape of a cell's /status
// or /healthz so one wedged cell can't stall an API list of every cell.
const cellControlTimeout = 3 * time.Second

// NewManager constructs the in-process manager. Caller must call
// Manager.Close to drain in-flight spawns + kill running tunnels
// on shutdown.
func NewManager(
	cfg Config,
	specs map[string]PoolSpec,
	router *Router,
	spawner Spawner,
) *Manager {
	pools := make(map[string]*PoolState, len(specs))
	for name, spec := range specs {
		pools[name] = NewPoolState(spec)
	}

	return &Manager{
		cfg:     cfg,
		router:  router,
		specs:   specs,
		pools:   pools,
		spawner: spawner,
		control: cellControlClient{
			http: &http.Client{Timeout: cellControlTimeout},
		},
		leases:  newLeaseRegistry(cfg.socksPublicAddr(), cfg.proxyLeaseTTL()),
		spawnMu: make(map[string]*sync.Mutex),
	}
}

// poolMutexFor returns the (lazy-init) spawn mutex for the given
// pool. Two goroutines spawning the same pool serialize through
// this; cross-pool spawns proceed in parallel.
func (m *Manager) poolMutexFor(name string) *sync.Mutex {
	m.spawnMuMu.Lock()
	defer m.spawnMuMu.Unlock()

	mu, ok := m.spawnMu[name]
	if !ok {
		mu = &sync.Mutex{}
		m.spawnMu[name] = mu
	}

	return mu
}

// AcquireForCountry resolves country → pool → tunnel. Spawns a
// fresh tunnel if the pool is cold. Returns the tunnel + the pool
// name so the caller can pass them back to Release when the HTTP
// exchange is done.
//
// excludeProxy + fallbackOK implement the retry-aware contract
// from the public client contract: a caller may exclude a
// previously-failed proxy and may allow escalation to the pool's
// fallback when the primary is exhausted.
func (m *Manager) AcquireForCountry(
	ctx context.Context,
	country string,
	excludeProxy *url.URL,
	fallbackOK bool,
) (Acquisition, error) {
	poolName, err := m.router.Resolve(country)
	if err != nil {
		return Acquisition{}, err
	}

	return m.acquireFromPool(ctx, poolName, excludeProxy, fallbackOK)
}

// AcquireForPool is the explicit-pool variant used by the
// POST /v1/proxies HTTP endpoint and by tests.
func (m *Manager) AcquireForPool(
	ctx context.Context,
	poolName string,
	excludeProxy *url.URL,
	fallbackOK bool,
) (Acquisition, error) {
	return m.acquireFromPool(ctx, poolName, excludeProxy, fallbackOK)
}

// acquireFromPool runs the resolve-or-spawn cycle for one pool,
// then falls through to the fallback pool when permitted +
// configured.
func (m *Manager) acquireFromPool(
	ctx context.Context,
	poolName string,
	excludeProxy *url.URL,
	fallbackOK bool,
) (Acquisition, error) {
	logger := ctxscope.GetLogger(ctx)

	state, ok := m.pools[poolName]
	if !ok {
		return Acquisition{}, ctxerrors.Wrapf(
			ErrUnknownPool, "%q", poolName,
		)
	}

	acq, err := m.acquireFromState(ctx, state, excludeProxy)
	if err == nil {
		return acq, nil
	}

	if !fallbackOK || state.Spec.FallbackPool == "" {
		return Acquisition{}, err
	}

	// Only escalate on conditions where another pool might help.
	// ErrInvalidCountry is a config bug; escalating doesn't fix it.
	if errors.Is(err, ErrInvalidCountry) {
		return Acquisition{}, err
	}

	logger.Warn(
		"primary pool exhausted; escalating to fallback",
		"pool", poolName,
		"fallback", state.Spec.FallbackPool,
		"err", err,
	)

	fallbackState, ok := m.pools[state.Spec.FallbackPool]
	if !ok {
		return Acquisition{}, ctxerrors.Wrapf(
			ErrUnknownPool,
			"fallback %q (from %q)", state.Spec.FallbackPool, poolName,
		)
	}

	// Fallback acquisition is NEVER allowed to escalate again —
	// one hop only, per the phase 10.5 retry spec.
	return m.acquireFromState(ctx, fallbackState, excludeProxy)
}

// acquireFromState tries the existing hot tunnel first, then
// spawns a fresh one when the pool is cold or the existing tunnel
// is the excluded one.
func (m *Manager) acquireFromState(
	ctx context.Context, state *PoolState, excludeProxy *url.URL,
) (Acquisition, error) {
	if t, err := state.acquire(excludeProxy); err == nil {
		return Acquisition{Tunnel: t, Pool: state.Spec.Name}, nil
	}

	// Pool cold (or only-hot-tunnel was excluded). Spawn a fresh
	// container. Serialized per-pool so concurrent proxy requests
	// for the same pool result in one spawn, not N.
	mu := m.poolMutexFor(state.Spec.Name)

	mu.Lock()
	defer mu.Unlock()

	// Re-check after acquiring the per-pool mutex — another
	// goroutine may have spawned while we were waiting.
	if t, err := state.acquire(excludeProxy); err == nil {
		return Acquisition{Tunnel: t, Pool: state.Spec.Name}, nil
	}

	return m.spawnFromState(ctx, state, excludeProxy)
}

func (m *Manager) spawnFromState(
	ctx context.Context, state *PoolState, excludeProxy *url.URL,
) (Acquisition, error) {
	conf, err := state.pickConf(
		time.Now(), excludeConfsFor(excludeProxy, state), m.cfg.FailureCacheTTL,
	)
	if err != nil {
		return Acquisition{}, err
	}

	spawnCtx, cancel := context.WithTimeout(ctx, m.cfg.SpawnTimeout)
	defer cancel()

	spawnStart := time.Now()

	tunnel, spawnErr := m.spawner.Spawn(spawnCtx, SpawnRequest{
		Pool:        state.Spec.Name,
		ConfName:    conf,
		ExitCountry: state.Spec.ExitCountries[conf],
		BundleDir:   m.cfg.BundleDir,
	})
	if spawnErr != nil {
		return Acquisition{}, m.recordSpawnFailure(ctx, state, conf, spawnErr)
	}

	TunnelSpawnsTotal.WithLabelValues(state.Spec.Name, metricOutcomeSuccess).Inc()
	TunnelSpawnDuration.WithLabelValues(state.Spec.Name).
		Observe(time.Since(spawnStart).Seconds())

	tunnel.Pool = state.Spec.Name

	tunnel.State = TunnelStateHot
	if tunnel.GatewayAddr == "" && tunnel.ProxyURL != nil {
		tunnel.GatewayAddr = tunnel.ProxyURL.Host
	}

	tunnel.InFlight = 1
	tunnel.LastUsedAt = time.Now()

	// Copy before publishing: once setTunnel stores the pointer as p.tunnel, a
	// concurrent acquire can mutate it under p.mu, so dereferencing *tunnel
	// after the publish would be an unsynchronized read (a -race finding).
	tunnelCopy := *tunnel
	state.setTunnel(tunnel)

	return Acquisition{
		Tunnel: &tunnelCopy,
		Pool:   state.Spec.Name,
	}, nil
}

func (m *Manager) recordSpawnFailure(
	ctx context.Context,
	state *PoolState,
	conf string,
	spawnErr error,
) error {
	outcome := metricOutcomeSpawnFail
	if errors.Is(spawnErr, ErrSpawnTimeout) {
		outcome = metricOutcomeTimeout
	}

	TunnelSpawnsTotal.WithLabelValues(state.Spec.Name, outcome).Inc()
	state.markFailed(conf, time.Now())

	ctxscope.GetLogger(ctx).Warn(
		"pool cell spawn failed",
		"pool", state.Spec.Name,
		"config", conf,
		"err", spawnErr,
	)

	return ctxerrors.Wrapf(
		ErrPoolUnavailable,
		"spawn pool %q conf %q: %s",
		state.Spec.Name, conf, spawnErr.Error(),
	)
}

// excludeConfsFor builds a conf-exclusion set from an excluded
// proxy URL: if the pool's current tunnel matches the URL, that
// tunnel's .conf becomes ineligible for the re-spawn so we don't
// hand back the same exit twice in a row.
func excludeConfsFor(
	excludeProxy *url.URL, state *PoolState,
) map[string]struct{} {
	out := make(map[string]struct{}, 1)
	if excludeProxy == nil {
		return out
	}

	t := state.Snapshot()
	if t != nil && t.ProxyURL != nil && sameURL(t.ProxyURL, excludeProxy) {
		out[t.ConfName] = struct{}{}
	}

	return out
}

// Release decrements the in-flight count for the tunnel returned
// by Acquire. Idempotent; safe to call from defer paths.
func (m *Manager) Release(acq Acquisition) {
	state, ok := m.pools[acq.Pool]
	if !ok {
		return
	}

	state.release()
}

// IssueLease turns an acquired cell into a short-lived controller-fronted
// SOCKS5 URL and records it as the pool's latest allocation.
func (m *Manager) IssueLease(acq Acquisition) (ProxyLease, error) {
	lease, err := m.leases.Issue(acq)
	if err != nil {
		return ProxyLease{}, err
	}

	state, ok := m.pools[acq.Pool]
	if !ok {
		return ProxyLease{}, ctxerrors.Wrapf(ErrUnknownPool, "%q", acq.Pool)
	}

	state.setLastURL(acq.Tunnel.ContainerID, lease.URL, lease.ExpiresAt)

	return lease, nil
}

// ResolveExcludedProxy maps a previously issued controller URL back to the
// cell URL used by pool rotation. Legacy direct cell URLs remain accepted.
func (m *Manager) ResolveExcludedProxy(raw string) (*url.URL, error) {
	proxyURL, err := url.ParseRequestURI(raw)
	if err != nil || proxyURL.Scheme != proxySchemeSOCKS5 || proxyURL.Host == "" || proxyURL.Port() == "" {
		return nil, ctxerrors.Wrap(ErrInvalidCountry, "invalid excluded proxy URL")
	}

	if proxyURL.User == nil {
		return proxyURL, nil
	}

	username := proxyURL.User.Username()

	password, ok := proxyURL.User.Password()
	if !ok {
		return nil, ctxerrors.Wrap(ErrInvalidCountry, "excluded controller proxy lacks password")
	}

	lease, ok := m.leases.Lookup(username, password)
	if !ok {
		return nil, ctxerrors.Wrap(ErrInvalidCountry, "excluded controller proxy is expired or unknown")
	}

	return lease.InternalURL, nil
}

// AcquireForLease validates a SOCKS5 lease and reserves its exact live cell
// until the returned acquisition is released when the proxied connection ends.
func (m *Manager) AcquireForLease(username, password string) (Acquisition, error) {
	lease, ok := m.leases.Lookup(username, password)
	if !ok {
		return Acquisition{}, ctxerrors.Wrap(ErrPoolUnavailable, "proxy lease expired or unknown")
	}

	state, ok := m.pools[lease.Pool]
	if !ok {
		return Acquisition{}, ctxerrors.Wrap(ErrPoolUnavailable, "proxy lease pool unavailable")
	}

	tunnel, err := state.acquireContainer(lease.ContainerID)
	if err != nil {
		return Acquisition{}, err
	}

	return Acquisition{Tunnel: tunnel, Pool: lease.Pool}, nil
}

// Close kills only the cells currently tracked by this manager. It is used on
// graceful service shutdown so a controller never leaves its own hot workers
// around for another process to discover and reconcile.
func (m *Manager) Close(ctx context.Context) {
	logger := ctxscope.GetLogger(ctx)

	for poolName, state := range m.pools {
		tunnel := state.Snapshot()
		if tunnel == nil || tunnel.ContainerID == "" {
			continue
		}

		if err := m.spawner.Kill(ctx, tunnel.ContainerID); err != nil {
			logger.Warn(
				"shutdown cell cleanup failed",
				"pool", poolName,
				"container", tunnel.ContainerID,
				"err", err,
			)
		}

		state.setTunnel(nil)
	}
}

// MarkFailed records that the given tunnel proved unhealthy in
// production. The reaper picks this up on its next tick + respawns.
func (m *Manager) MarkFailed(poolName, confName string) {
	state, ok := m.pools[poolName]
	if !ok {
		return
	}

	state.mu.Lock()

	if state.tunnel != nil && state.tunnel.ConfName == confName {
		state.tunnel.State = TunnelStateUnhealthy
	}

	state.mu.Unlock()
	state.markFailed(confName, time.Now())
}

// Views returns admin-readable PoolView entries for /v1/pools.
func (m *Manager) Views() []PoolView {
	now := time.Now()
	out := make([]PoolView, 0, len(m.pools))

	for name, state := range m.pools {
		view := PoolView{
			Name:         name,
			Region:       state.Spec.Region,
			FallbackPool: state.Spec.FallbackPool,
			ConfCount:    len(state.Spec.Configs),
			RecentlyFailed: state.RecentlyFailedCount(
				now, m.cfg.FailureCacheTTL,
			),
		}

		if t := state.Snapshot(); t != nil {
			view.Tunnel = tunnelViewOf(t, now)
		}

		out = append(out, view)
	}

	sort.Slice(out, func(left, right int) bool {
		return out[left].Name < out[right].Name
	})

	return out
}

// ProxyViews returns one flattened entry per currently running tunnel. The
// list is bounded by configured pools, so total is an in-memory exact count.
func (m *Manager) ProxyViews() []ProxyView {
	now := time.Now()
	views := make([]ProxyView, 0, len(m.pools))

	for poolName, state := range m.pools {
		tunnel := state.Snapshot()
		if tunnel == nil {
			continue
		}

		views = append(views, proxyViewOf(poolName, tunnel, now))
	}

	sort.Slice(views, func(left, right int) bool {
		return views[left].Pool < views[right].Pool
	})

	return views
}

// Pools returns the underlying state map so the reaper + health
// monitor can iterate. Read-only contract.
func (m *Manager) Pools() map[string]*PoolState { return m.pools }

// Spawner is the seam used to abstract docker container spawn.
// Production uses CellSpawner (docker SDK). Tests inject a
// deterministic in-memory spawner.
type Spawner interface {
	Spawn(ctx context.Context, req SpawnRequest) (*Tunnel, error)
	Kill(ctx context.Context, containerID string) error

	// ListChildren discovers this controller's live cells straight from docker
	// (by the pr0xteus.parent.id label), so the source of truth for which cells
	// exist and where they are is docker, not in-memory pool state.
	ListChildren(ctx context.Context) ([]CellHandle, error)
}

// CellHandle is one cell as docker currently reports it: its container ID,
// origin labels, docker state, and the cellproxy control base URL resolved from
// its current ephemeral IP on the cell network. ControlURL is nil when the cell
// has no reachable control address (e.g. host-loopback smoke mode).
type CellHandle struct {
	ContainerID string
	Pool        string
	ConfName    string
	State       string
	ControlURL  *url.URL
	CreatedAt   time.Time
}

// SpawnRequest is the input to Spawner.Spawn. The spawner is told
// the pool name + .conf name + bundle dir; everything else (config,
// network mode, ports, env) it gets from its own constructor-time
// dependencies.
type SpawnRequest struct {
	Pool        string
	ConfName    string
	ExitCountry string
	BundleDir   string
}

// Acquisition is the value returned by Manager.AcquireForX. Holds
// a *Tunnel copy (safe to mutate locally) + the resolved pool name
// the caller passes back to Release.
type Acquisition struct {
	Tunnel *Tunnel
	Pool   string
}

// tunnelViewOf projects a Tunnel into TunnelView shape.
func tunnelViewOf(t *Tunnel, now time.Time) *TunnelView {
	var proxyURL string
	if t.ProxyURL != nil {
		proxyURL = t.ProxyURL.String()
	}

	return &TunnelView{
		ConfName:    t.ConfName,
		ProxyURL:    proxyURL,
		State:       t.State,
		ExitCountry: t.ExitCountry,
		ExitIP:      t.ExitIP,
		SpawnedAt:   t.SpawnedAt,
		HealthyAt:   t.HealthyAt,
		LastUsedAt:  t.LastUsedAt,
		IdleSeconds: now.Sub(t.LastUsedAt).Seconds(),
	}
}

func proxyViewOf(poolName string, tunnel *Tunnel, now time.Time) ProxyView {
	return ProxyView{
		Pool:             poolName,
		ConfName:         tunnel.ConfName,
		State:            tunnel.State,
		ExitCountry:      tunnel.ExitCountry,
		ExitIP:           tunnel.ExitIP,
		SpawnedAt:        tunnel.SpawnedAt,
		HealthyAt:        tunnel.HealthyAt,
		LastUsedAt:       tunnel.LastUsedAt,
		LastURL:          tunnel.LastURL,
		LastURLExpiresAt: tunnel.LastURLExpiresAt,
		IdleSeconds:      now.Sub(tunnel.LastUsedAt).Seconds(),
	}
}

// PoolsDir returns the bundle directory the manager was configured
// with. Mostly useful for diagnostics + integration tests.
func (m *Manager) PoolsDir() string {
	return filepath.Clean(m.cfg.BundleDir)
}
