---
title: "Home"
description: "pr0xteus gives trusted apps and containers private WireGuard exits as short-lived SOCKS5 and HTTP proxy URLs, with cells started on demand in Docker."
nav_order: 1
permalink: /
seo:
  type: WebSite
---

# pr0xteus

Private, on-demand WireGuard exits for applications and containers. Give pr0xteus authorized WireGuard configurations, then request short-lived, credentialed SOCKS5 and HTTP proxy URLs from approved pools. Your host does not become a VPN client, and callers never receive provider configuration.

## How it works

| Part | Does |
| --- | --- |
| Controller | `psyb0t/pr0xteus`. Serves the bearer-protected control API on port 8000, picks a pool configuration and starts or reuses its cell. |
| Cell | One container per pool, one WireGuard file each. Waits for a handshake behind a default-drop firewall, then serves a private SOCKS5 listener. |
| Proxy gateways | SOCKS5 on port 1080 and HTTP on port 8080, on the controller. They check the lease credentials and relay to the cell. |
| Socket proxy | The only container with the Docker socket, limited to the calls the controller needs. |
| Metrics | `/healthz` and Prometheus `/metrics` on port 9091. |
| Tailscale sidecar | Optional. Private remote access without publishing host ports. |

The supplied stack binds every port to host loopback. The controller removes idle or unhealthy cells and reports traffic per cell. Details are in [controller internals](controller.md) and [security boundaries](security.md).

## Quick start

You need Linux, Docker with Compose and an authorized WireGuard `.conf` file. No source checkout or host Go installation is needed.

Review and run the [installer](https://github.com/psyb0t/pr0xteus/blob/main/install.sh):

```bash
curl -fsSL https://raw.githubusercontent.com/psyb0t/pr0xteus/main/install.sh | bash
```

It installs `~/.local/bin/pr0xteus`, creates owner-only configuration under `~/.config/pr0xteus/` and generates the API token. Copy your WireGuard file:

```bash
cp /path/to/your-vpn.conf ~/.config/pr0xteus/secrets/wireguard/us.conf
```

Configure the pool and routing:

```yaml
# ~/.config/pr0xteus/secrets/pools.yaml
pools:
  us:
    region: north-america
    purpose: private-service-egress
    configs: [us]
    exit_countries:
      us: US
```

```yaml
# ~/.config/pr0xteus/config/egress-routing.yaml
country_to_pool:
  US: us
default_pool: us
```

Start the stack:

```bash
pr0xteus start
pr0xteus status
```

## Allocate an exit

This Bash example needs `curl` and `jq`. It reads the token from the installed configuration rather than putting it in shell history:

```bash
token="$(sed -n 's/^PR0XTEUS_API_TOKEN=//p' ~/.config/pr0xteus/.env)"
auth=(--header @<(printf 'Authorization: Bearer %s' "$token"))

allocation="$(
  curl --fail-with-body --request POST "${auth[@]}" \
    --header 'Content-Type: application/json' \
    --data '{"country":"US"}' \
    http://127.0.0.1:8000/v1/proxies
)"
socks5_proxy="$(jq -er '.proxies.socks5' <<<"$allocation")"
http_proxy="$(jq -er '.proxies.http' <<<"$allocation")"

curl --fail --silent --show-error --proxy "$socks5_proxy" https://api.ipify.org
curl --fail --silent --show-error --proxy "$http_proxy" https://api.ipify.org

unset token allocation socks5_proxy http_proxy
unset -a auth
```

Both URLs share the selected cell, lease credentials and expiry. DNS and outbound traffic happen through the cell's WireGuard tunnel. Returned URLs are credentials too: do not log or share them.

## Where to go next

| Page | What it covers |
| --- | --- |
| [Complete setup walkthrough](complete-example.md) | Install, configure, allocate and prove egress step by step |
| [Installation and upgrades](deploy.md) | Per-user and system-wide installs, the `pr0xteus` command, upgrades, Tailscale |
| [Configuration](configuration.md) | Pool and routing files, every `.env` and controller variable, cell image pairing |
| [Direct Docker deployment](manual-deployment.md) | Running the stack without the wrapper |
| [Control API](api.md) | Allocation, inventories, cell teardown, errors |
| [Go client](go-client.md) | `pkg/client` with retries, modes and preflight |
| [Agent integrations](integrations.md) | The skill for Claude Code, Codex and OpenClaw |
| [Monitoring](monitoring.md) | Health, Prometheus metrics, logs |
| [Troubleshooting](troubleshooting.md) | Startup refusals and failed allocations |
| [Security boundaries](security.md) | What is isolated and what you have to protect |
| [Controller internals](controller.md) | How the controller and cells work |
| [Development](development.md) | Building and testing from source, CI |
