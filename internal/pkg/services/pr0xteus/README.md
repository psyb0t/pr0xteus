# Internal control plane

This package holds the non-public orchestrator implementation. External users
talk to the HTTP API or import `pkg/client`; nothing outside this repository
should rely on `internal` types.

## Responsibility split

```text
api.go       request authentication, strict JSON, transport errors
config.go    environment and token-file validation
routing.go   country-to-pool policy parsing
pool.go      pool state, acquire/release, failure cache, projections
spawner.go   constrained Docker cell lifecycle
reaper.go    idle, unhealthy, and orphan cleanup
service.go   HTTP listeners and lifecycle wiring
metrics.go   bounded-label Prometheus collectors
```

`APIServer` owns HTTP shape. `Manager` owns pool choice and synchronization.
`CellSpawner` owns Docker details. Keeping these boundaries separate prevents a
request from growing into arbitrary Docker control.

## Acquisition invariants

- A request selects exactly one country or named pool.
- A pool exposes at most one hot tunnel in this version.
- Concurrent cold requests for the same pool serialize around the spawn path,
  so they do not create a container stampede.
- Recently failed configuration names are skipped for the configured TTL.
- A caller can exclude a returned SOCKS5 URL to avoid getting the same config
  back during replacement.
- `Release` completes bookkeeping after a URL is returned. It does not track
  a caller's downstream SOCKS5 session; idle time is a warm-cache policy, not
  a connection lease.

Pool configuration is immutable for one process lifetime. Edit local policy
and rebuild/restart the Compose stack when changing it.

## Cell contract

The spawner receives a configuration basename selected by the manager. It
constructs the host path itself from the configured bundle directory, mounts
that exact file read-only at `/wgconf/wg0.conf`, and does not accept a file
path from the API layer.

When `PR0XTEUS_CELL_NETWORK` is set, a cell has no published host port. The
controller probes `<cell-name>:1080` on the private Docker network and returns
that private URL. With no cell network, the controlled host-loopback mode
uses an ephemeral loopback binding for direct local smoke tests.

Every spawned cell gets a dedicated name and labels:

- `pr0xteus.managed=true`
- `pr0xteus.pool=<logical-pool>`
- `pr0xteus.conf=<config-basename>`

The labels let a fresh controller remove stale leftovers that it cannot safely
adopt because its in-memory pool state starts cold.

## Docker hardening boundary

The actual cell needs `NET_ADMIN` and `/dev/net/tun`; that is the smallest
known capability/device set for WireGuard setup. It also gets `cap_drop=ALL`,
`no-new-privileges`, PID 1 init, log rotation, CPU/memory/PID limits, a single
read-only config bind, and required network sysctls. It does not receive the
Docker socket or a caller-controlled environment.

The controller itself reaches a Docker socket proxy rather than a mounted raw
socket. `compose.yaml` must keep both the proxy's permission allow-list and
the controller's network topology in sync with the Docker calls in
`spawner.go`.

## Error and observability contract

Package sentinels in `errors.go` separate bad policy (`ErrUnknownPool`,
`ErrInvalidCountry`) from temporary capacity/spawn conditions
(`ErrPoolUnavailable`, `ErrPoolExhausted`). The HTTP boundary maps those to a
stable JSON error envelope without exporting Docker error text to callers.

Metrics labels are all bounded by known pool names, state, outcome, or reason.
Never add a config name, container ID, client address, or request ID as a
Prometheus label.
