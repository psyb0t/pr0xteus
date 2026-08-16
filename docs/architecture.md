# Architecture

pr0xteus is a private controller-fronted proxy. The controller decides *which*
tunnel a caller gets, authenticates a short-lived SOCKS5 lease, and relays that
connection to the selected cell. It never has an egress-network route, resolves
client DNS, or talks to a destination directly. This document is the
data-path/control-path split and the component
boundaries. Route detail lives in [docs/api.md](api.md), deployment lives in
[docs/deploy.md](deploy.md), the full setup+egress walkthrough is
[docs/complete-example.md](complete-example.md), and implementation detail is
in the [controller README](../internal/pkg/services/pr0xteus/README.md) and
the [cell README](../cell/README.md).

## Control path vs. data path

```text
                     control path (HTTP, bearer token)
trusted client ─────────────────────────────► controller ──► socket proxy ──► dockerd
                                                  │ validates request, picks a pool, spawns/monitors cells
                                                  │
data path (lease-authenticated SOCKS5)             │ private cell-control network
trusted client ─────────────────────────────► controller ───────────────────► cell ──► wg0 ──► VPN peer
                                                  │                              SOCKS5 and DNS
                                                  └── no egress-network attachment or destination DNS
```

`POST /v1/proxies` returns a
`socks5://<lease-id>:<lease-secret>@127.0.0.1:1080` URL by default. The caller
dials that URL from the host or another reachable trusted client; the
controller validates the one-cell lease and connects to that cell's private
SOCKS listener across `pr0xteus-cell-control`. The selected cell resolves the
destination and sends traffic through `wg0`. The controller continues its
control-path work too: discovering cells, scraping traffic snapshots, and
deciding when to reap them.

## Components

**Controller** (`internal/pkg/services/pr0xteus/`) — one Go service. It:

- validates `POST /v1/proxies` bodies, selects an approved pool/config, and
  issues a random, short-lived SOCKS5 lease only after the cell's WireGuard
  handshake completed and its SOCKS5 listener is open
  (`spawner.go` `waitReady`/`probeSocks5`);
- authenticates the lease and relays TCP CONNECT to that exact cell through
  the internal cell-control network without resolving destination DNS;
- owns two background loops plus an orphan reconciler run by the `Reaper`:
  an idle loop that kills cells past `IdleTimeout` with zero in-flight
  requests *and* no live connections reported by cellproxy, and a health
  loop that probes each hot cell's `/healthz` and marks it for respawn on
  failure (`reaper.go`);
- exposes the control API and a separate `/metrics` (Prometheus) + `/healthz`
  listener, both loopback-bound by the supplied Compose stack (`api.go`,
  `docker-compose.yml`).

The controller reaches Docker only through **docker-socket-proxy**
(`tecnativa/docker-socket-proxy`), which holds the raw `/var/run/docker.sock`
and exposes only `CONTAINERS=1`/`POST=1` — enough to create, start, stop,
inspect, and list containers, nothing else (`docker-compose.yml`). The
controller talks to it at `tcp://docker-socket-proxy:2375`
(`PR0XTEUS_DOCKER_HOST`).

**Cell** — one disposable container per active tunnel: one WireGuard peer
plus `cellproxy`, this repo's own SOCKS5 proxy. It gets exactly `NET_ADMIN`,
`SETUID`, `SETGID`, `/dev/net/tun`, and one read-only WireGuard config bind
mount; every other capability is dropped. It starts as root to build the
`wg0` interface, firewall, and routes, then drops to UID 1500 for the
SOCKS5/control daemon (`cell/entrypoint.sh`, `spawner.go`
`buildHostConfig`). The controller spawns it lazily on `POST /v1/proxies`
and kills it via the reaper or `DELETE /v1/cells/{id}`.

**cellproxy** (`internal/pkg/cellproxy/`) — the binary the cell runs. SOCKS5
via `things-go/go-socks5`, dialing outbound through the container's `wg0`
interface. Every dial goes through a recording wrapper: a failed dial marks
`DialFailed` (used as a liveness signal), a successful one gets a
byte/request-counting `net.Conn` so `/status` can report per-destination
traffic (`proxy.go` `dial`, `conn.go`). A second HTTP server on the same
process serves `/healthz` (plain liveness) and `/status` (uptime + traffic
snapshot, `CellID`/`ParentID` echoed back) — the entrypoint firewall accepts
both the control port and SOCKS5 port on the internal cell-control interface
for the controller gateway, plus SOCKS5 on egress for an intentional direct
Docker-network deployment. Neither port is accepted on `wg0` (the tunnel side)
(`cell/entrypoint.sh` step 7).

## Discovery and reaping: Docker labels, not a registry

The controller keeps no persistent list of "cells I own." Every cell it
spawns carries `pr0xteus.managed=true`, `pr0xteus.pool`, `pr0xteus.conf`,
`pr0xteus.scope`, and `pr0xteus.parent.id=<controller container ID>` as
Docker labels (`spawner.go` `buildContainerConfig`, `LabelParent`); the
controller reads its own container ID from its hostname at startup, which
under Docker *is* the short container ID (`config.go` `resolveParentID`),
and falls back to `pr0xteus.scope` when no parent ID is set. `GET /v1/cells`
and the reaper's loops all call `ListChildren`, which lists containers by
that label filter straight from Docker (`spawner.go` `ListChildren`,
`cells.go` `Cells`/`CellByID`). A restarted controller rediscovers its live
cells on the next tick — nothing to rebuild from a database — and
`pr0xteus.scope` keeps two controllers sharing a daemon from ever touching
each other's cells. Each cell's control address is resolved on demand from its
current IP on the internal cell-control network, never cached
(`spawner.go` `controlURLFromSummary`).

Reaping uses real signals, not just a timer. `reapIdle` requires `IdleTimeout`
elapsed *and* zero in-flight requests *and* no live connections in the cell's
own `/status` traffic snapshot (`hasLiveConnections`, `reaper.go`) — a cell
mid-download past its idle timeout is left alone. Health reaping hits the
cell's real `/healthz`; in the degraded host-loopback smoke-test mode (no
reachable control address) it falls back to a handshake-age heuristic
instead (`reaper.go` `probeHealthy`). A separate orphan loop reconciles
Docker against in-memory pool state every minute and on boot, killing
anything labelled `pr0xteus.managed=true` under this controller's scope that
the pool no longer tracks — the safety net for crashed spawns and process
restarts (`reaper.go` `orphanLoop`, `spawner.go` `ReapOrphans`).

## Networks and trust boundaries

Four Docker networks, declared in `docker-compose.yml`:

- `pr0xteus-control` — `internal: true`. Controller ↔ socket-proxy ↔
  (optional) Tailscale sidecar. No route out of Docker.
- `pr0xteus-cell-control` — `internal: true`. Carries controller↔cell health
  checks, traffic status, and controller-gateway→cell SOCKS5 traffic. Cells
  join it alongside egress. The controller does **not** join egress, so an
  RCE/SSRF in the controller has no NAT route out of Docker.
- `pr0xteus-egress` — not internal; cells need real internet reachability to
  dial their configured WireGuard endpoint *before* the tunnel exists. Direct
  Docker-network consumers may join it intentionally, but normal callers use
  the controller's published SOCKS gateway. The controller never joins it.
- `pr0xteus-tailnet` — optional, only reachable by the Tailscale sidecar
  (`--profile tailscale`), for exposing the control API off-host without a
  published port. See [Tailscale](../README.md#tailscale) and
  [docs/deploy.md](deploy.md).

The controller API (`:8000`), metrics listener (`:9091`), and SOCKS gateway
(`:1080`) publish only to `127.0.0.1` in the supplied Compose stack
(`docker-compose.yml` `ports:`). Everything downstream of the HTTP binding —
bearer-token validation and pool selection — is controller-trusted. The SOCKS
gateway admits only random per-allocation leases, forwards only TCP CONNECT,
and leaves destination resolution and egress to the selected WireGuard cell.

## Kill-switch

A cell that never gets a WireGuard handshake never becomes a working proxy —
it doesn't fail open. `entrypoint.sh` sets default-DROP on `INPUT`/`OUTPUT`/
`FORWARD` before anything else runs, allows only loopback, established
connections, and the WireGuard handshake to the pre-resolved endpoint IP,
brings up `wg0`, and blocks on `wait_for_handshake` before opening the
SOCKS5/control ports or starting `cellproxy` at all. DNS is switched to the
tunnel-supplied resolver only after the handshake lands. Default route goes
through `wg0`; the pre-tunnel `/32` route to the endpoint is the only thing
that still uses the container's own interface (`cell/entrypoint.sh`, steps
2–9). See the [cell README](../cell/README.md) for the full boot sequence
and privilege-drop detail.

## What this document is not

It is not a route reference (`docs/api.md`), a deploy runbook
(`docs/deploy.md`), or a line-by-line tour of either Go package
(`internal/pkg/services/pr0xteus/README.md`,
[cell/README.md](../cell/README.md)). Read those for the parts you're
actually about to touch.
