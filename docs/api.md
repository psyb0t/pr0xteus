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

## Error behaviour

The service uses the project-standard JSON error envelope. Expect:

- `401` — missing or incorrect bearer token.
- `400` — invalid request shape, unknown pool, or unmapped country with no
  configured default.
- `415` — missing or non-JSON content type.
- `503` — no usable tunnel could be selected or started.
- `500` — unexpected server failure; inspect controller logs without printing
  credentials or WireGuard configuration.

`GET /healthz` and `GET /metrics` live on the separate metrics listener
(default `:9091`), not under `/v1`, and are intentionally not authenticated.
Keep that listener private.
