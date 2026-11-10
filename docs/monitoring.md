---
title: "Monitoring"
description: "pr0xteus health and Prometheus metrics on port 9091, the per-cell traffic view, log fields and where to read the logs of the controller and wrapper."
nav_order: 7
---

# Monitoring

## Health

`GET /healthz` on the metrics listener (host `127.0.0.1:9091` by default) answers `ok` while the controller runs. The Compose healthcheck uses it. It does not prove that a WireGuard tunnel can be allocated right now; a test allocation does that.

```bash
curl --fail --silent http://127.0.0.1:9091/healthz
```

The listener has no authentication. Keep it on loopback or behind tailnet policy.

## Prometheus metrics

`GET /metrics` on the same listener serves the Go runtime metrics plus these collectors from [`metrics.go`](https://github.com/psyb0t/pr0xteus/blob/main/internal/pkg/services/pr0xteus/metrics.go):

| Metric | Type | Labels | Meaning |
| --- | --- | --- | --- |
| `pr0xteus_tunnel_pool_spawns_total` | counter | `pool`, `outcome` | Cell spawn attempts. `outcome` is `success`, `timeout` or `spawn_fail`. |
| `pr0xteus_tunnel_pool_spawn_seconds` | histogram | `pool` | Time from container create to a healthy cell. Buckets from 1 to 30 seconds. |
| `pr0xteus_tunnel_pool_hot_tunnels` | gauge | `pool` | Hot cells in the pool, 0 or 1. |
| `pr0xteus_tunnel_pool_reaps_total` | counter | `pool`, `reason` | Cells removed by the reaper. `reason` is `idle` or `unhealthy`. |
| `pr0xteus_tunnel_pool_acquires_total` | counter | `pool`, `outcome` | Successful allocations (`outcome="ok"`), counted against the pool that served them, which may be the fallback. |

Labels never include configuration names, container IDs, client addresses or destinations.

## Per-cell traffic

[`GET /v1/cells`](api.md#get-v1cells) lists the running cells with request counts, bytes up and down, active connections, dial failures and the top destinations of each. It needs the bearer token.

## Logs

The controller logs JSON through `slog` (`LOG_FORMAT`, `LOG_LEVEL`, `LOG_ADD_SOURCE` in `.env`). Each record carries `binary`, `commit`, `version` and `service`. Allocation events name the requested country, the pool and the exit country; spawn and reap events add the configuration basename and container ID. The controller does not log tokens, lease secrets or proxy URLs.

```bash
pr0xteus logs --follow
pr0xteus logs --tail=200
```

Compose caps the controller and socket proxy logs at five 10 MB files each, and every cell gets the same cap. The wrapper also appends its own output to `${TMPDIR:-/tmp}/pr0xteus-command.log`.
