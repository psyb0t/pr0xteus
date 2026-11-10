---
title: "Configuration"
description: "pr0xteus operator files, pools.yaml and egress-routing.yaml fields, every .env and controller environment variable with defaults, and cell image pairing."
nav_order: 1
parent: "Installation and upgrades"
---

# Configuration

All sensitive or provider-specific material lives under your config directory (`~/.config/pr0xteus/` per-user, `/etc/pr0xteus/` system-wide), stays out of Git, and stays out of Docker build contexts:

- `secrets/wireguard/*.conf`: real WireGuard files.
- `secrets/pools.yaml`: named pools and their approved config basenames.
- `config/egress-routing.yaml`: country-to-pool policy.
- `.env`: the private bearer token, absolute host configuration path, runtime user, ports and controller image selection.

The installer writes `.env`; it is not something you need to create by hand. `pr0xteus setup` and `pr0xteus upgrade` refresh `.env.example` next to it, so new settings show up there first. The controller reads the pool and routing files once at startup, so restart the stack after changing them.

## pools.yaml

```yaml
pools:
  primary:
    region: operator-defined
    purpose: general-outbound
    configs: [my-provider-node, my-provider-node-2]
    exit_countries:
      my-provider-node: US
      my-provider-node-2: US
    fallback_pool: backup
  backup:
    region: operator-defined
    purpose: general-outbound
    configs: [backup-node]
    exit_countries:
      backup-node: US
```

| Field | Required | Meaning |
| --- | --- | --- |
| `pools.<name>` | yes | Logical pool name. Callers can request it with `{"pool":"<name>"}`. At least one pool must exist. |
| `configs` | yes | Basenames of files in `secrets/wireguard/` without `.conf`. Every name must exist when the controller starts. |
| `exit_countries` | no | Maps a basename to its ISO 3166-1 alpha-2 exit country. Every key must be one of the pool's `configs`. Without an entry, the controller guesses the country from a legacy `<country>-<location>` file name. |
| `fallback_pool` | no | Pool to try when this one is exhausted or unavailable and the caller sent `"fallbackOk": true`. It must name a defined pool. |
| `region`, `purpose` | no | Free text shown in `GET /v1/pools`. |

A pool runs at most one cell at a time. Configurations that just failed are skipped for `TUNNEL_POOL_FAILURE_CACHE_TTL` before the controller tries them again.

## egress-routing.yaml

```yaml
country_to_pool:
  US: primary
default_pool: primary
```

`country_to_pool` maps a requested country code (case does not matter) to a pool. `default_pool` handles countries with no entry. Every pool named here must exist in `pools.yaml`.

Keep `default_pool` set. When it is missing, the controller falls back to `TUNNEL_POOL_DEFAULT_POOL`, whose default is `neutral_clean`, and refuses to start unless a pool with that name exists. `config check` does not apply that fallback, so it can pass while startup fails. See [troubleshooting](troubleshooting.md#the-controller-exits-at-startup).

## .env settings

These are the values the installer writes and the wrapper and Compose read. [`.env.example`](https://github.com/psyb0t/pr0xteus/blob/main/.env.example) holds the current list.

| Variable | Default | Meaning |
| --- | --- | --- |
| `PR0XTEUS_CONFIG_DIR` | set by the installer | Absolute host path of the config directory. Docker mounts it into the controller at the same path, so the controller can ask Docker to bind one WireGuard file into a cell. |
| `PR0XTEUS_RUNTIME_USER` | installing user's `UID:GID` | User the controller runs as, so it can read owner-only config files. `setup` and `upgrade` keep it current. |
| `PR0XTEUS_API_TOKEN` | random, generated | Bearer token for the control API. Required, at most 4096 bytes. |
| `PR0XTEUS_CONTROLLER_IMAGE` | the latest release | Controller image. `setup` and `upgrade` pin it to a release tag. |
| `PR0XTEUS_HTTP_HOST_PORT` | `127.0.0.1:8000` | Host binding for the control API, as `HOST:PORT`. |
| `PR0XTEUS_METRICS_HOST_PORT` | `127.0.0.1:9091` | Host binding for `/healthz` and `/metrics`. |
| `PR0XTEUS_SOCKS_HOST_PORT` | `127.0.0.1:1080` | Host binding for the SOCKS5 gateway. |
| `PR0XTEUS_HTTP_PROXY_HOST_PORT` | `127.0.0.1:8080` | Host binding for the HTTP proxy gateway. |
| `PR0XTEUS_SOCKS_PUBLIC_ADDRESS` | `127.0.0.1:1080` | `host:port` written into returned SOCKS5 URLs. Never `0.0.0.0`. |
| `PR0XTEUS_HTTP_PROXY_PUBLIC_ADDRESS` | `127.0.0.1:8080` | `host:port` written into returned HTTP proxy URLs. Never `0.0.0.0`. |
| `PR0XTEUS_DISABLE_HOST_PORTS` | `false` | `true` removes every controller host port. Use it with the Tailscale sidecar or another private gateway. |
| `PR0XTEUS_TAILSCALE_ENABLED` | `false` | `true` starts the Tailscale sidecar profile. See [Tailscale sidecar](deploy.md#tailscale-sidecar). |
| `TS_AUTHKEY` | placeholder | Auth key from your tailnet, used on the sidecar's first login. |
| `TS_HOSTNAME` | `pr0xteus` | Tailnet host name of the sidecar. |
| `TS_EXTRA_ARGS` | `--accept-dns=false` | Extra `tailscale up` arguments, such as `--login-server` for Headscale or `--advertise-tags`. |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn` or `error`. |
| `LOG_FORMAT` | `json` | `json` or `text`. |
| `LOG_ADD_SOURCE` | `true` | Include the source location in log records. |

The wrapper also reads `PR0XTEUS_HOME` from your shell to find a config directory in a non-default place.

## Controller environment

The controller binary reads these variables (`internal/pkg/services/pr0xteus/config.go`). The supplied [`docker-compose.yml`](https://github.com/psyb0t/pr0xteus/blob/main/docker-compose.yml) sets the ones it needs from `.env`, so a normal install never sets them by hand. They matter when you run the image some other way.

| Variable | Default | Meaning |
| --- | --- | --- |
| `PR0XTEUS_API_TOKEN` | none, required | Bearer token. Empty or longer than 4096 bytes fails startup. |
| `TUNNEL_POOL_LISTEN_ADDR` | `:8000` | Control API listener. |
| `TUNNEL_POOL_METRICS_ADDR` | `:9091` | `/healthz` and `/metrics` listener. |
| `TUNNEL_POOL_SOCKS_ADDR` | `:1080` | SOCKS5 gateway listener. |
| `TUNNEL_POOL_SOCKS_PUBLIC_ADDR` | `127.0.0.1:1080` | Address in returned SOCKS5 URLs. A host is required. |
| `TUNNEL_POOL_HTTP_PROXY_ADDR` | `:8080` | HTTP proxy gateway listener. |
| `TUNNEL_POOL_HTTP_PROXY_PUBLIC_ADDR` | `127.0.0.1:8080` | Address in returned HTTP proxy URLs. A host is required. |
| `TUNNEL_POOL_PROXY_LEASE_TTL` | `15m` | How long returned proxy URLs stay valid. Must be positive. |
| `TUNNEL_POOL_POOLS_FILE` | `secrets/pools.yaml` | Pool policy file. |
| `TUNNEL_POOL_BUNDLE_DIR` | `secrets/wireguard` | Directory of WireGuard `.conf` files. |
| `TUNNEL_POOL_ROUTING_FILE` | `config/egress-routing.yaml` | Country routing file. |
| `TUNNEL_POOL_DEFAULT_POOL` | `neutral_clean` | Default pool used only when the routing file has no `default_pool`. |
| `TUNNEL_POOL_IDLE_TIMEOUT` | `10m` | Unused time before a cell with no active connections is removed. |
| `TUNNEL_POOL_SPAWN_TIMEOUT` | `20s` | How long a new cell has to become healthy. |
| `TUNNEL_POOL_HEALTH_CHECK_INTERVAL` | `30s` | How often the controller calls each hot cell's `/healthz`. |
| `TUNNEL_POOL_HEALTH_HANDSHAKE_MAX_AGE` | `180s` | Host-loopback test mode only: age of the last healthy check after which a cell counts as unhealthy. |
| `TUNNEL_POOL_FAILURE_CACHE_TTL` | `5m` | How long a failed configuration is skipped. |
| `PR0XTEUS_CELL_NETWORK` | empty | Docker network cells join for egress. Compose sets `pr0xteus-egress`. Empty selects a host-loopback test mode. |
| `PR0XTEUS_CELL_CONTROL_NETWORK` | empty | Internal network between the controller and cells. Compose sets `pr0xteus-cell-control`. |
| `PR0XTEUS_CELL_SOCKS_PORT` | `1080` | SOCKS5 port inside each cell. |
| `PR0XTEUS_CELL_CONTROL_PORT` | `9090` | Control HTTP port inside each cell. Must differ from the SOCKS5 port. |
| `PR0XTEUS_MANAGED_SCOPE` | `default` | Scope label that separates this controller's cells from another controller on the same Docker daemon. Compose sets `pr0xteus-compose`. Must not be empty. |
| `PR0XTEUS_PARENT_ID` | the container host name | Controller identity stamped on each cell, used to find its children again. |
| `PR0XTEUS_DOCKER_HOST` | empty (Docker default) | Docker API endpoint. Compose points it at `tcp://docker-socket-proxy:2375`. |

## Cell image pairing

The controller chooses its cell image itself, from the build version baked into its binary: `psyb0t/pr0xteus:cell-<version>`. CI bakes `sha-<commit>`, so the published `:latest` controller and the release tag promoted from it both pull the cell built from the same commit (`cell-sha-<commit>`). Releases up to v0.11.6 were rebuilt on their tag and pull `cell-vX.Y.Z` instead. A local development build uses `dev` and pulls `psyb0t/pr0xteus:cell-dev`. There is no environment setting for the cell image.

To stay on a particular release, set `PR0XTEUS_CONTROLLER_IMAGE=psyb0t/pr0xteus:vX.Y.Z` in `~/.config/pr0xteus/.env`, run `pr0xteus setup`, then run `pr0xteus start`.

See [installation and upgrades](deploy.md) for installer paths, upgrades, ports and tailnet access.
