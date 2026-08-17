# Control API

The control API is a small private HTTP API. It is versioned under `/v1`,
requires `Authorization: Bearer <token>` on every route, and is loopback-bound
by default in the supplied Compose stack. `PR0XTEUS_DISABLE_HOST_PORTS=true`
removes every host binding for a private Docker-network gateway such as the
Tailscale sidecar. `POST /v1/proxies` allocates one SOCKS5 lease; `GET
/v1/proxies` lists active exits. The shared path is intentional: POST is the
state-changing collection action and GET is the read-only collection view.

For a safe manual request and a real SOCKS5 egress proof, follow
[complete-example.md](complete-example.md). The handler and pool state behind
these routes are documented in [internal/README.md](../internal/pkg/services/pr0xteus/README.md).

## `POST /v1/proxies`

Request exactly one selection method:

```json
{"country":"US","fallbackOk":true}
```

```json
{"pool":"primary","excludeProxy":"socks5://lease-id:lease-secret@127.0.0.1:1080"}
```

Fields:

- `country` — ISO 3166-1 alpha-2 code resolved by local routing policy.
- `pool` — exact local logical pool name. It bypasses country routing.
- `excludeProxy` — optional previous SOCKS5 URL. The matching configuration is
  excluded while finding a replacement.
- `fallbackOk` — permits a configured fallback pool when the selected pool is
  exhausted or unavailable.

Requests must use `Content-Type: application/json`. Bodies are capped at 16 KiB,
unknown fields are rejected, and a request with both—or neither—`country` and
`pool` gets a validation error.

Successful response (`200 OK`):

```json
{
  "url": "socks5://lease-id:lease-secret@127.0.0.1:1080",
  "pool": "primary",
  "exitCountry": "US",
  "expiresAt": "2026-01-01T00:15:00Z"
}
```

The URL points at the controller's SOCKS5 gateway, carries a random short-lived
username/password lease, and routes only to the exact cell selected for this
allocation. Keep it out of logs. A lease never silently switches to a
replacement cell. `exitIP` is reserved optional metadata and is normally
omitted; the controller does not make an external exit-IP lookup. Do not make
correctness depend on it.

## `GET /v1/proxies`

Returns the flattened inventory of currently active tunnels. It is paginated
with `limit` (default `100`, maximum `1000`) and `offset` (default `0`). It is
read-only: it never starts a cell and never issues a new lease.

```json
{
  "proxies": [
    {
      "pool": "primary",
      "confName": "edge-a",
      "state": "hot",
      "exitCountry": "US",
      "spawnedAt": "2026-01-01T00:00:00Z",
      "healthyAt": "2026-01-01T00:00:05Z",
      "lastUsedAt": "2026-01-01T00:01:00Z",
      "lastURL": "socks5://lease-id:lease-secret@127.0.0.1:1080",
      "lastURLExpiresAt": "2026-01-01T00:16:00Z",
      "idleSeconds": 12.3
    }
  ],
  "limit": 100,
  "offset": 0,
  "total": 1
}
```

`lastURL` is omitted until an allocation has been made. It is a live
credential, so this endpoint has the same bearer-token protection as allocation.

## `GET /v1/pools`

Returns the authenticated operator view of configured pools and their current
tunnel projections. It is paginated with `limit` (default `100`, maximum
`1000`) and `offset` (default `0`). Docker IDs and in-flight request
bookkeeping deliberately do not cross this API boundary.

Typical response:

```json
{
  "pools": [
    {
      "name": "primary",
      "region": "operator-defined",
      "confCount": 2,
      "recentlyFailed": 0,
      "tunnel": {
        "confName": "edge-a",
        "proxyUrl": "socks5://pr0xteus-tunnel-primary-123:1080",
        "state": "hot",
        "exitCountry": "US"
      }
    }
  ],
  "limit": 100,
  "offset": 0,
  "total": 1
}
```

This is an operator view, not the client-facing proxy inventory. `proxyUrl` is
the cell's internal SOCKS endpoint; use `POST /v1/proxies` for a client URL and
`GET /v1/proxies` for allocation history.

## `GET /v1/cells`

Lists a page of running cells with live traffic snapshots. It is paginated with
`limit` (default `100`, maximum `1000`) and `offset` (default `0`). Cells are
discovered straight from Docker — the controller queries for containers carrying
its own `pr0xteus.parent.id` label, so the source of truth for which cells exist
and where they are is Docker, not in-memory state. Each cell's traffic is then
scraped on demand from its cellproxy `/status` control endpoint at the cell's
current IP on the private cell network. The view is keyed by the cell's own
Docker container ID so a specific cell can be inspected or destroyed. `state`
is Docker's own container state (e.g. `running`).

```json
{
  "cells": [
    {
      "containerId": "9f3c1a2b4d5e",
      "parentId": "controller-host-id",
      "pool": "primary",
      "confName": "edge-a",
      "state": "running",
      "exitCountry": "US",
      "createdAt": "2026-08-14T20:00:00Z",
      "uptimeSeconds": 128.4,
      "traffic": {
        "requests": 42,
        "bytesUp": 18234,
        "bytesDown": 918273,
        "active": 3,
        "dialFailures": 0,
        "destinations": [
          {"destination": "example.com:443", "requests": 40, "bytesUp": 18000, "bytesDown": 900000, "active": 3}
        ]
      }
    }
  ],
  "limit": 100,
  "offset": 0,
  "total": 1
}
```

`traffic` is omitted and a `statusError` string is set when the controller could
not reach a cell's control server — for example in the host-loopback smoke mode,
where cells expose no control address. The destination breakdown is bounded and
byte-ranked (`PR0XTEUS_CELL_TOP_DESTINATIONS`, default 50) so the response stays
small under heavy fan-out.

## `GET /v1/cells/{containerID}`

Returns the single cell with the given container ID, in the same shape as one
entry of `GET /v1/cells`. Responds `404` when no tracked cell matches.

## `DELETE /v1/cells/{containerID}`

Destroys a cell on demand: stops its container and clears its pool slot so the
next request re-spawns. Responds `204` on success and `404` when no tracked cell
matches. Only destroy a cell your own task allocated.

## Error behaviour

The service uses the project-standard JSON error envelope. Expect:

- `401` — missing or incorrect bearer token.
- `400` — invalid request shape, unknown pool, or unmapped country with no
  configured default.
- `404` — no cell matches the requested container ID on a `/v1/cells/{id}` route.
- `415` — missing or non-JSON content type.
- `503` — no usable tunnel could be selected or started.
- `500` — unexpected controller failure, including an unavailable Docker
  discovery call; inspect controller logs without printing credentials or
  WireGuard configuration.

`GET /healthz` and `GET /metrics` live on the separate metrics listener
(default `:9091`), not under `/v1`, and are intentionally not authenticated.
Keep that listener private.
