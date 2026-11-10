---
title: "Troubleshooting"
description: "Fix pr0xteus startup refusals, config check errors, 401 and 503 responses, cells that never become healthy, and unreachable proxy URLs, with the exact messages."
nav_order: 8
---

# Troubleshooting

Start with the logs and the config check:

```bash
pr0xteus logs --tail=200
pr0xteus status
```

`pr0xteus start` runs the image's `config check` before it starts anything, so most configuration mistakes stop there with one of the messages below.

## The controller exits at startup

The controller validates its configuration in `New` and exits before it touches Docker. The message names the file and the problem:

| Message | Fix |
| --- | --- |
| `pools.yaml has no pools` | Add at least one pool under `pools:`. |
| `pool "<name>" has no configs` | List at least one WireGuard basename in that pool's `configs`. |
| `pool "<name>" references "<conf>", not in <dir>` | Put `<conf>.conf` in `secrets/wireguard/`, or fix the basename. Names are the file name without `.conf`. |
| `exit_countries names config "<conf>" outside its pool` | Every `exit_countries` key must also appear in that pool's `configs`. |
| `exit_countries["<conf>"] must be an ISO 3166-1 alpha-2 code` | Use a two-letter country code such as `US`. |
| `pool "<name>" fallback "<other>" not defined` | Define the fallback pool or remove `fallback_pool`. |
| `country <cc> routes to unknown pool "<pool>"` | Every pool in `country_to_pool` must exist in `pools.yaml`. |
| `default pool "<pool>" is not in pools.yaml` | Set `default_pool` in `egress-routing.yaml` to a defined pool. |
| `API token is empty` or `API token exceeds 4096 bytes` | Set a valid `PR0XTEUS_API_TOKEN` in `.env`. |
| `<VARIABLE> must be host:port`, `<VARIABLE> host is required`, `<VARIABLE> port must be in 1..65535` | Fix that address in `.env` or the controller environment. Public addresses need a real host, never an empty one. |

A message about `default pool "neutral_clean"` means `egress-routing.yaml` has no `default_pool`, so the controller fell back to the built-in `TUNNEL_POOL_DEFAULT_POOL` value. `config check` does not catch this case. Add `default_pool` to the routing file.

`docker compose` itself refuses to start with `run pr0xteus setup first` when `.env` lacks `PR0XTEUS_CONTROLLER_IMAGE`, `PR0XTEUS_RUNTIME_USER`, `PR0XTEUS_API_TOKEN` or `PR0XTEUS_CONFIG_DIR`. Run `pr0xteus setup`.

## config check fails on the token

`PR0XTEUS_API_TOKEN is required in .env` means the `.env` in your config directory has no token line. The installer generates one; restore it from your backup or set a long random value yourself.

## 401 from the API

The `Authorization` header is missing, does not start with `Bearer `, or carries a different token from the one in `.env`. The controller reads the token at startup, so restart the stack after changing it.

## 503 on POST /v1/proxies

The pool had no healthy cell and could not start one within `TUNNEL_POOL_SPAWN_TIMEOUT` (20 seconds by default), or every configuration in it is excluded or recently failed. Check the logs around the request:

- `pull cell image psyb0t/pr0xteus:cell-...` errors mean Docker could not pull the cell image that matches this controller. Check the host's network access to Docker Hub.
- A spawn timeout usually means the WireGuard handshake never arrived: the endpoint is unreachable, the file's keys are wrong, or the provider rejected the peer. The controller logs the cell's startup output on failure.
- Failed configurations are skipped for `TUNNEL_POOL_FAILURE_CACHE_TTL` (5 minutes). Add more configurations to the pool, or a `fallback_pool` and `"fallbackOk": true`, to keep serving while one provider node is down.

## 400 on POST /v1/proxies

The body is not one JSON object with exactly one of `country` or `pool`, the country is not two letters, the pool or country route does not exist, or `excludeProxy` is not an unexpired controller SOCKS5 URL. The response is always the same `VALIDATION_FAILED` envelope, so compare the request with the [API reference](api.md).

## The proxy URL does not connect

Returned URLs use `PR0XTEUS_SOCKS_PUBLIC_ADDRESS` and `PR0XTEUS_HTTP_PROXY_PUBLIC_ADDRESS`. They default to loopback, so they only work on the Docker host. For another machine, publish the ports on a protected address and set both public addresses to something that machine can reach, or use the [Tailscale sidecar](deploy.md#tailscale-sidecar). Lease URLs also expire after `TUNNEL_POOL_PROXY_LEASE_TTL` (15 minutes); request a new one.

## /v1/cells shows statusError

The controller could not reach that cell's control server. In the host-loopback test mode cells have no control address, so this is expected there. In a normal stack it usually means the cell is shutting down or has crashed; the reaper removes it on its next pass.
