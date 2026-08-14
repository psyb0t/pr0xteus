// Package pr0xteus implements logical pools of cell-backed WireGuard tunnels
// with on-demand spawn, idle reap, and an authenticated HTTP control API.
package pr0xteus

import "errors"

// Sentinel errors returned by pr0xteus. Callers
// (the public client over HTTP, and the in-process Service surface)
// route behavior on these via errors.Is.
var (
	// ErrPoolUnavailable — the pool has no healthy tunnel and could
	// not spawn one within the configured timeout. Surfaces to the
	// HTTP caller as 503 Service Unavailable.
	ErrPoolUnavailable = errors.New("tunnel pool unavailable")

	// ErrPoolExhausted — every candidate tunnel has been excluded
	// (recent-failure cache or caller's excludeProxy) and no
	// fallback is permitted. Distinct from ErrPoolUnavailable in
	// that it is NOT a spawner-side failure; it means "you've
	// already tried every option in this pool, give up".
	ErrPoolExhausted = errors.New("tunnel pool exhausted")

	// ErrUnknownPool — the named pool isn't in the loaded
	// pools.yaml. Configuration mismatch, not a runtime condition.
	ErrUnknownPool = errors.New("unknown pool")

	// ErrInvalidCountry — the requested country code didn't resolve
	// to any pool and no default pool is configured. Configuration
	// mismatch.
	ErrInvalidCountry = errors.New("invalid country / no pool configured")

	// ErrSpawnTimeout — cell container failed to report a healthy
	// handshake within the configured spawn timeout. The container is
	// killed and the .conf is marked bad in the pool's
	// recent-failure cache so subsequent picks try a different one.
	ErrSpawnTimeout = errors.New("cell spawn timeout")

	// ErrSpawnFailed — docker run failed entirely (image pull,
	// daemon unreachable, etc.). Distinct from ErrSpawnTimeout
	// because there's nothing to clean up — the container never
	// came up.
	ErrSpawnFailed = errors.New("cell spawn failed")

	// ErrTunnelUnhealthy — a previously-healthy tunnel stopped
	// reporting a recent WireGuard handshake. The health monitor
	// uses this to drive reap + respawn. Reaches HTTP callers only
	// indirectly: the pool service returns 503 until the spawn
	// loop has produced a new healthy tunnel.
	ErrTunnelUnhealthy = errors.New("tunnel unhealthy")

	// ErrConfigInvalid — pools.yaml or egress-routing.yaml failed
	// schema validation at load time. Service refuses to start.
	ErrConfigInvalid = errors.New("invalid pr0xteus config")

	// ErrBundleConfMissing — a .conf referenced in pools.yaml is
	// absent from the WireGuard bundle directory. Config
	// + filesystem mismatch; service refuses to start.
	ErrBundleConfMissing = errors.New("wireguard conf missing from bundle")
)
