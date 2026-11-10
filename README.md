# pr0xteus

[![CI](https://github.com/psyb0t/pr0xteus/actions/workflows/pipeline.yml/badge.svg?branch=main)](https://github.com/psyb0t/pr0xteus/actions/workflows/pipeline.yml)
[![version](https://raw.githubusercontent.com/psyb0t/pr0xteus/badges/version.svg)](https://github.com/psyb0t/pr0xteus/releases)
[![license](https://raw.githubusercontent.com/psyb0t/pr0xteus/badges/license.svg)](https://github.com/psyb0t/pr0xteus/blob/main/LICENSE)
[![coverage](https://raw.githubusercontent.com/psyb0t/pr0xteus/badges/coverage.svg)](https://github.com/psyb0t/pr0xteus/actions/workflows/pipeline.yml)
[![Docker Pulls](https://img.shields.io/docker/pulls/psyb0t/pr0xteus?style=flat-square)](https://hub.docker.com/r/psyb0t/pr0xteus)

Private, on-demand WireGuard exits for applications and containers. Give pr0xteus authorized WireGuard configurations, then request short-lived, credentialed SOCKS5 and HTTP proxy URLs from approved pools. Your host does not become a VPN client, and callers never receive provider configuration.

## How it works

A controller selects an approved configuration and starts an isolated Docker cell through a restricted socket proxy. The cell waits for a WireGuard handshake, then serves its private proxy. Clients use the controller's lease-authenticated SOCKS5 or HTTP gateway. The controller reports traffic and destinations and removes idle or unhealthy cells.

The supplied stack binds API, metrics and proxy ports to host loopback. An optional Tailscale sidecar provides private remote access without publishing host ports. See [controller internals](https://psyb0t.github.io/pr0xteus/controller/) and [security boundaries](https://psyb0t.github.io/pr0xteus/security/).

## Quick start

You need Linux, Docker with Compose and an authorized WireGuard `.conf` file. No source checkout or host Go installation is needed.

Review and run the [installer](https://github.com/psyb0t/pr0xteus/blob/main/install.sh):

```bash
curl -fsSL https://raw.githubusercontent.com/psyb0t/pr0xteus/main/install.sh | bash
```

It installs `~/.local/bin/pr0xteus`, creates owner-only configuration under `~/.config/pr0xteus/` and generates the API token. The installer selects the latest tagged release and its matching cell image.

Copy your WireGuard file:

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

The default API is `http://127.0.0.1:8000`. Health and metrics use `http://127.0.0.1:9091`. Edit the generated `.env` for ports and operator settings. See [installation and upgrades](https://psyb0t.github.io/pr0xteus/deploy/#install-it) for system-wide installs, rolling images, permissions and preserved configuration.

## Allocate an exit and use it

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

Both URLs share the selected cell, lease credentials and expiry. They target the controller, not an unpublished cell address. DNS and outbound traffic happen through the cell's WireGuard tunnel. Returned URLs are credentials too: do not log or share them. The [complete walkthrough](https://psyb0t.github.io/pr0xteus/complete-example/) covers egress proof, bad allocations and readiness checks.

## Private remote access

The optional Tailscale or Headscale sidecar has its own persistent tailnet identity. API bearer authentication and proxy lease credentials still apply. The metrics listener has no application authentication, so restrict its access with tailnet ACLs. See [Tailscale setup](https://psyb0t.github.io/pr0xteus/deploy/#tailscale-sidecar).

## API and agent integrations

The JSON API allocates leases, lists approved pools, inventories live cells and their traffic, and destroys selected cells. Collection endpoints use `limit` and `offset`. Read [the API reference](https://psyb0t.github.io/pr0xteus/api/) for request, response and error contracts.

A public [Go client](https://psyb0t.github.io/pr0xteus/go-client/) handles allocation and retries. Documentation skills are available for Claude Code, Codex and OpenClaw; see [agent integrations](https://psyb0t.github.io/pr0xteus/integrations/). pr0xteus is an HTTP service, not an MCP server.

## Development

Source development uses Docker-backed Make targets:

```bash
make help
make lint
make test-coverage
make sec
make generate
```

Tests use the race detector and self-contained Testcontainers infrastructure. Real-provider tests are opt-in. See [development and testing](https://psyb0t.github.io/pr0xteus/development/) for image builds, coverage, production-image API tests and local stack commands.

## Documentation

The full documentation is at **[psyb0t.github.io/pr0xteus](https://psyb0t.github.io/pr0xteus/)**.

| Section | What it covers |
| --- | --- |
| [Complete setup walkthrough](https://psyb0t.github.io/pr0xteus/complete-example/) | Install, configure, allocate and prove egress |
| [Installation and upgrades](https://psyb0t.github.io/pr0xteus/deploy/) | Installer modes, the `pr0xteus` command, upgrades, Tailscale |
| [Configuration](https://psyb0t.github.io/pr0xteus/configuration/) | Pool and routing files, every setting, cell image pairing |
| [Direct Docker deployment](https://psyb0t.github.io/pr0xteus/manual-deployment/) | Running the stack without the wrapper |
| [Control API](https://psyb0t.github.io/pr0xteus/api/) | Requests, responses and errors |
| [Go client](https://psyb0t.github.io/pr0xteus/go-client/) | `pkg/client` retries, modes and preflight |
| [Agent integrations](https://psyb0t.github.io/pr0xteus/integrations/) | Claude Code, Codex and OpenClaw |
| [Monitoring](https://psyb0t.github.io/pr0xteus/monitoring/) | Health, metrics and logs |
| [Troubleshooting](https://psyb0t.github.io/pr0xteus/troubleshooting/) | Startup refusals and failed allocations |
| [Security boundaries](https://psyb0t.github.io/pr0xteus/security/) | What is isolated and what you protect |
| [Controller internals](https://psyb0t.github.io/pr0xteus/controller/) | Controller and cell design |
| [Development](https://psyb0t.github.io/pr0xteus/development/) | Building, testing and CI |

The [changelog](https://github.com/psyb0t/pr0xteus/blob/main/CHANGELOG.md) lists every release.

## Sponsor

<a href="https://www.buymeacoffee.com/psyb0t"><img src="https://cdn.buymeacoffee.com/buttons/v2/default-blue.png" alt="Buy Me a Coffee" height="60" width="217"></a>

## License and notices

The project source is [MIT licensed](https://github.com/psyb0t/pr0xteus/blob/main/LICENSE). [Third-party notices](https://github.com/psyb0t/pr0xteus/blob/main/THIRD_PARTY_NOTICES.md) and `vendor/` contain dependency licenses and attribution.
