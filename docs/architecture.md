# Architecture

pr0xteus is a small control plane for short-lived, WireGuard-backed SOCKS5
proxies. It does not accept arbitrary Docker or network instructions from an
API caller: the operator supplies a local pool policy, and the caller can only
request a configured country or pool.

Want the operator-facing version with actual files and commands? Read
[complete-example.md](complete-example.md). This document explains why the
boundaries exist; the controller state machine is in
[internal/README.md](../internal/README.md), and the firewall and boot detail
is in [cell/README.md](../cell/README.md).

## Components

```text
authenticated caller
        │ POST /v1/proxies
        ▼
pr0xteus orchestrator ── restricted Docker socket proxy ── Docker daemon
        │                         │
        │ private egress network  │ only container operations
        ▼                         ▼
cell: WireGuard + microSocks ── WireGuard endpoint
```

- **Orchestrator** — a non-root Go service that validates the request, chooses
  a configured WireGuard file, starts at most one hot cell per pool, and
  reaps idle or unhealthy cells.
- **Docker socket proxy** — the only Compose service allowed to see the raw
  Docker socket. It exposes only the Docker API subset needed for cell
  lifecycle operations to the orchestrator.
- **Cell** — a one-tunnel container. It receives one read-only WireGuard file,
  brings up `wg0`, installs a default-drop firewall, waits for a handshake,
  then drops to an unprivileged user and serves SOCKS5.

The code-level state machine, Docker settings, and cleanup rules are in
[`internal/README.md`](../internal/README.md). The cell boot sequence is in
[`cell/README.md`](../cell/README.md).

## Network boundaries

The Compose stack uses two explicit networks:

- `pr0xteus-control` is internal. It connects the controller to the restricted
  socket proxy; no host port or public egress is exposed from that network.
- `pr0xteus-egress` lets cells reach their WireGuard peers. The controller also
  joins it solely to probe a freshly-started cell's private SOCKS5 listener.

The controller's API and metrics ports bind to `127.0.0.1` on the host. Put a
separate authenticated reverse proxy or tailnet endpoint in front only when
you genuinely need remote access.

## Pool policy

`secrets/pools.yaml` is local and ignored. A pool contains a list of approved
configuration basenames, with an optional fallback pool. `config/egress-routing.yaml`
maps requested ISO country codes to these logical pools.

The default filename convention is `<country>-<location>.conf`, which produces
the exit country from its prefix. That is a compatibility fallback, not a
provider requirement: use `exit_countries` in a pool for arbitrary filenames.

```yaml
pools:
  primary:
    configs: [edge-a, edge-b]
    exit_countries:
      edge-a: GB
      edge-b: GB
```

The API cannot select a filename, bind mount, image, network, or Docker option.
Those are configuration-time concerns owned by the operator.

## Lifecycle

1. A caller submits an authenticated request containing exactly one of
   `country` or `pool`.
2. The manager uses the routing policy, reuses a suitable hot tunnel, or picks
   a non-recently-failed configuration from that pool.
3. The cell is created with `NET_ADMIN`, `/dev/net/tun`, a read-only config
   file, resource caps, bounded logs, and no Docker socket.
4. The controller returns a SOCKS5 URL only after the cell has completed its
   handshake wait and accepts a private TCP probe.
5. The idle reaper removes unused or unhealthy cells. A fresh process reaps
   only cells labelled as leftovers from the same controller-managed scope,
   never cells managed by another controller on the same Docker daemon.

The returned SOCKS5 URL is a capability to use one private cell, not a claim
that the cell remains alive forever. A client should request a replacement
when a SOCKS connection fails, passing the old URL as `excludeProxy`.

## Observability

`/metrics` exposes Prometheus counters, gauges, and durations for spawn,
acquire, and reap decisions. `/healthz` says the metrics listener is serving;
it does not prove that a provider tunnel is currently available. Operator API
requests are logged as structured JSON without bearer tokens or config values.
