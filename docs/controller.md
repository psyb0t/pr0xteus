---
title: "Controller internals"
description: "How the pr0xteus controller is split: API server, pool manager, lease registry, proxy gateways, Docker cell spawner and reaper, with their invariants."
nav_order: 10
has_children: true
---

# Controller internals

The controller lives in [`internal/pkg/services/pr0xteus`](https://github.com/psyb0t/pr0xteus/tree/main/internal/pkg/services/pr0xteus). It is not a public Go API. External users talk to the HTTP API or import the [Go client](go-client.md); nothing outside this repository should rely on `internal` types.

## Responsibility split

```text
api.go                 request authentication, strict JSON, transport errors
config.go              environment and bearer-token validation
bootstrap.go           image CLI configuration bootstrap and validation
routing.go             country-to-pool policy parsing
pool.go                pool state, acquire/release, failure cache, projections
lease.go               short-lived lease credentials behind returned URLs
proxy_gateway.go       lease-authenticated SOCKS5 gateway
http_proxy_gateway.go  lease-authenticated HTTP forward proxy and CONNECT
spawner.go             constrained Docker cell lifecycle
reaper.go              idle, unhealthy, and orphan cleanup
service.go             listeners and lifecycle wiring
metrics.go             bounded-label Prometheus collectors
cells.go               paginated /v1/cells list, fetch, and delete handlers
cellstatus.go          per-cell traffic and health snapshot projection
```

`APIServer` owns HTTP shape. `Manager` owns pool choice and synchronization. `CellSpawner` owns Docker details. Keeping these boundaries separate prevents a request from growing into arbitrary Docker control.

Every collection route (`/v1/proxies`, `/v1/pools`, and `/v1/cells`) uses the same bounded `limit`/`offset`/`total` envelope. Docker discovery failures are returned to the HTTP layer as `500`; they are never misrepresented as an empty collection or a missing cell.

## Startup and shutdown

`New` loads the configuration, `pools.yaml`, `egress-routing.yaml` and the Docker client, and fails before any container starts when one of them is invalid. `Run` then removes leftover cells labelled for this controller, starts the reaper, and opens four listeners: the control API, the SOCKS5 gateway, the HTTP proxy gateway and the metrics listener. `Stop` closes the listeners, stops the reaper and kills the cells it still tracks.

## Leases and gateways

An allocation picks a cell, then the lease registry issues a random username and secret for it and keeps only a SHA-256 digest of the secret. Both returned URLs (`socks5://` and `http://`) carry the same credentials, share one expiry (`TUNNEL_POOL_PROXY_LEASE_TTL`, default 15 minutes) and route to that one cell.

The SOCKS5 gateway and the HTTP proxy gateway check the lease, then relay the connection to the cell's private SOCKS5 listener. The controller never resolves a destination name or dials a destination itself; the cell does both through WireGuard. The HTTP gateway handles absolute-form HTTP requests and `CONNECT` tunnels.

## Acquisition invariants

- A request selects exactly one country or named pool.
- A pool exposes at most one hot tunnel in this version.
- Concurrent cold requests for the same pool serialize around the spawn path, so they do not create a container stampede.
- Recently failed configuration names are skipped for the configured TTL.
- A caller can exclude a returned SOCKS5 URL to avoid getting the same config back during replacement.
- `Release` completes allocation bookkeeping after a URL is returned. The controller gateways separately keep the exact cell in flight until the relayed connection closes, so idle reap cannot cut an active stream.

Pool configuration is immutable for one process lifetime. Edit local policy and restart the stack when changing it.

## Cell contract

The spawner receives a configuration basename selected by the manager. It constructs the host path itself from the configured bundle directory, mounts that exact file read-only at `/wgconf/wg0.conf`, and does not accept a file path from the API layer.

When `PR0XTEUS_CELL_NETWORK` is set, a cell has no published host port. The controller reaches the cell's SOCKS5 and control ports by name on the private Docker networks and keeps that address internal; callers only ever get controller URLs. With no cell network, the controlled host-loopback mode uses an ephemeral loopback binding for direct local smoke tests.

Every spawned cell gets a dedicated name and labels:

- `pr0xteus.managed=true`
- `pr0xteus.pool=<logical-pool>`
- `pr0xteus.conf=<config-basename>`
- `pr0xteus.scope=<managed-scope>`
- `pr0xteus.parent.id=<parent-controller-id>` (the discovery key a fresh controller uses to find its own children)

The labels let a fresh controller remove stale leftovers that it cannot safely adopt because its in-memory pool state starts cold.

## Reaper

The reaper runs three loops. The idle loop removes a cell that has gone unused for `TUNNEL_POOL_IDLE_TIMEOUT` and, according to its `/status`, carries no live connections. The health loop calls each hot cell's control `/healthz` every `TUNNEL_POOL_HEALTH_CHECK_INTERVAL`, removes a cell that does not answer, and skips its configuration for the failure-cache TTL. A cell with no control address (host-loopback mode) counts as unhealthy once its last healthy time is older than `TUNNEL_POOL_HEALTH_HANDSHAKE_MAX_AGE`. The orphan loop removes managed containers in this scope that are not a tracked hot tunnel, such as the residue of a failed spawn.

## Docker hardening boundary

The cell needs `NET_ADMIN`, `DAC_READ_SEARCH`, `SETUID`, `SETGID`, and `/dev/net/tun`: WireGuard setup, reading the owner-only WireGuard file, and `su-exec`'s one-way drop to an unprivileged UID. It also gets `cap_drop=ALL`, `no-new-privileges`, PID 1 init, log rotation (10 MB, 5 files), limits of 128 MiB memory, half a CPU and 64 PIDs, a single read-only config bind, and the required network sysctls. It does not receive the Docker socket or a caller-controlled environment.

The controller itself reaches a Docker socket proxy rather than a mounted raw socket. [`docker-compose.yml`](https://github.com/psyb0t/pr0xteus/blob/main/docker-compose.yml) must keep both the proxy's permission allow-list and the controller's network topology in sync with the Docker calls in `spawner.go`.

## Error and observability contract

Package sentinels in `errors.go` separate bad policy (`ErrUnknownPool`, `ErrInvalidCountry`) from temporary capacity and spawn conditions (`ErrPoolUnavailable`, `ErrPoolExhausted`). The HTTP boundary maps those to a stable JSON error envelope without exporting Docker error text to callers.

Metrics labels are all bounded by known pool names, outcome, or reason. Never add a config name, container ID, client address, or request ID as a Prometheus label. See [monitoring](monitoring.md).
