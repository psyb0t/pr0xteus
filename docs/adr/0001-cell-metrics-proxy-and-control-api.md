# ADR 0001 — Cell metrics proxy and a spec-first control API

Status: accepted (in progress)

## Context

Three limitations, all rooted in one architectural fact — **the controller is
not in the data path**. `POST /v1/proxies` hands a caller a SOCKS5 URL and the
caller connects directly to `microsocks` inside the cell.

1. **No traffic observability.** `microsocks` exposes no metrics and only logs
   connections to stderr. The controller therefore cannot report per-cell
   request counts, bytes, or destinations. Prometheus metrics today cover only
   allocation/lifecycle (spawns, reaps, acquires, hot gauge, spawn duration).
2. **Health checking is a TTL, not a probe.** `Reaper.probeHealthy` only checks
   `now - HealthyAt > HealthHandshakeMaxAge`; it never probes the SOCKS5 port or
   the WireGuard handshake. A cell whose tunnel silently died keeps being handed
   out until the max-age timer fires.
3. **Reaping is not session-aware.** `handleProxy` calls `Release` immediately
   after returning the URL, so `InFlight` is ~always 0 and idle-reap is driven
   by last *allocation* time, not live traffic. A long-lived connection that was
   not re-allocated within `IdleTimeout` can be reaped underneath itself.

The control API is two hand-written endpoints on stdlib `http.ServeMux` with
`aichteeteapee` helpers. There is no way to list all cells, destroy one on
demand, or see per-cell traffic.

## Decision

Replace `microsocks` with a first-party **cell proxy** and move the control
plane to a spec-first (OpenAPI + `oapi-codegen`) API, matching gitrakz.

### 1. `cmd/cellproxy` — the metrics-emitting SOCKS5 proxy

A small static Go binary run inside the cell instead of `microsocks`:

- SOCKS5 CONNECT via a vetted library (`things-go/go-socks5`), dialing through
  the cell's default route (wg0) — the kill-switch iptables from the entrypoint
  are unchanged, the proxy still only reaches the internet through the tunnel.
- A metrics-recording dialer wraps every target connection to count bytes
  up/down, increment a request counter, and record the destination host:port
  and the live-connection count, aggregated per destination.
- A **control HTTP server bound to the cell's internal (control) interface
  only** — never the egress side — exposing:
  - `GET /healthz` — real liveness: proxy accepting connections AND the wg
    handshake is fresh (`wg show` age within bound). Replaces the TTL heuristic.
  - `GET /stats` — JSON: totals (requests, bytes up/down, active connections)
    and a bounded top-N destination breakdown.

This one component fixes all three limitations: real health, live-session
awareness for reaping, and per-cell request/byte/destination metrics.

### 2. Controller changes

- A cell-stats client scrapes each hot cell's `/healthz` (real health probe) and
  `/stats` (aggregated into the tunnel view), over the internal control network.
- `Reaper` uses `/healthz` for health and the live-connection count from `/stats`
  for session-aware idle-reap (do not reap a cell with active connections).
- Tunnel/PoolView gain traffic fields sourced from `/stats`.

### 3. Spec-first control API

- Add `oapi-codegen` to the `go.mod` tool block; author `openapi.yaml` via
  `cmd/apigen` (as gitrakz does); generate server interface + types + a client
  SDK; wire generated handlers onto `aichteeteapee`.
- Existing: `POST /v1/proxies`, `GET /v1/pools`. New:
  - `GET /v1/cells` — flat list of every cell across pools, with state, exit,
    and the `/stats` traffic view.
  - `DELETE /v1/cells/{id}` — destroy a cell on demand (reuses the reaper's
    `killTunnel`).

## Consequences

- New first-party cell binary and one age-gated, vendored SOCKS5 dependency.
- The cell exposes a control port, reachable only on the internal control
  network (the egress side stays SOCKS5-only, kill-switched).
- The controller grows a scraper loop; the API becomes generated with a typed
  client SDK for consumers (edgefinder, trailing-stops).
- Cell image carries a static Go binary instead of the microsocks C binary —
  comparable size.

## Rejected alternatives

- **Docker container net-stats only** — gives bytes per cell without touching
  the cell, but no request counts and no destinations.
- **Keep `microsocks`, parse its stderr** — fragile, unstructured, and still no
  byte accounting.

## Phases

1. `cmd/cellproxy` binary (SOCKS5 + metrics + control HTTP); cell image +
   entrypoint run it instead of microsocks.
2. Controller: cell-stats client, real health probe, session-aware reaping.
3. Spec-first API: `openapi.yaml` + `oapi-codegen`, `GET /v1/cells`,
   `DELETE /v1/cells/{id}`, stats in views.
4. Generated client SDK, docs, tests, release.
