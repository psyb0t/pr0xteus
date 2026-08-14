# Control API

The control API is a small private HTTP API. It is versioned under `/v1`,
requires `Authorization: Bearer <token>` on every route, and is loopback-bound
by the supplied Compose stack.

For a safe manual request and a real private-network SOCKS5 proof, follow
[complete-example.md](complete-example.md). The handler and pool state behind
these routes are documented in [internal/README.md](../internal/README.md).

## `POST /v1/proxies`

Request exactly one selection method:

```json
{"country":"US","fallbackOk":true}
```

```json
{"pool":"primary","excludeProxy":"socks5://pr0xteus-tunnel-primary-123:1080"}
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

Successful response:

```json
{
  "url": "socks5://pr0xteus-tunnel-primary-123:1080",
  "pool": "primary",
  "exitCountry": "US"
}
```

`exitIP` is reserved optional metadata and is normally omitted; the controller
does not make an external exit-IP lookup. Do not make correctness depend on it.

## `GET /v1/pools`

Returns the authenticated operator view of configured pools and their current
tunnel projections. Docker IDs and in-flight request bookkeeping deliberately
do not cross this API boundary.

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
  ]
}
```

This is an operator view, not a public proxy directory. The proxy hostname is
usable only from the private egress Docker network.

## `GET /v1/cells`

Lists every running cell with its live traffic snapshot. Cells are discovered
straight from Docker — the controller queries for containers carrying its own
`pr0xteus.parent.id` label, so the source of truth for which cells exist and
where they are is Docker, not in-memory state. Each cell's traffic is then
scraped on demand from its cellproxy `/status` control endpoint at the cell's
current IP on the private cell network. The view is keyed by the cell's own
Docker container ID so a specific cell can be inspected or destroyed. `state` is
Docker's own container state (e.g. `running`).

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
  ]
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
- `500` — unexpected server failure; inspect controller logs without printing
  credentials or WireGuard configuration.

`GET /healthz` and `GET /metrics` live on the separate metrics listener
(default `:9091`), not under `/v1`, and are intentionally not authenticated.
Keep that listener private.
