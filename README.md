# pr0xteus

[![CI](https://github.com/psyb0t/pr0xteus/actions/workflows/pipeline.yml/badge.svg?branch=main)](https://github.com/psyb0t/pr0xteus/actions/workflows/pipeline.yml)
[![version](https://raw.githubusercontent.com/psyb0t/pr0xteus/badges/version.svg)](https://github.com/psyb0t/pr0xteus/releases)
[![license](https://raw.githubusercontent.com/psyb0t/pr0xteus/badges/license.svg)](LICENSE)
[![coverage](https://raw.githubusercontent.com/psyb0t/pr0xteus/badges/coverage.svg)](https://github.com/psyb0t/pr0xteus/actions/workflows/pipeline.yml)
[![Docker Pulls](https://img.shields.io/docker/pulls/psyb0t/pr0xteus?style=flat-square)](https://hub.docker.com/r/psyb0t/pr0xteus)

Your Docker services need to leave through a VPN, but you do not want to hand
them a provider account, turn the host into a VPN client, or accidentally run
an open proxy. pr0xteus is the small private control plane between those
things: give it WireGuard files you are allowed to use, and trusted containers
get short-lived SOCKS5 exits from the pools you approve. Every live exit is
observable — see how many requests and bytes went through each cell and to which
destinations, and destroy any of them on demand.

## Contents

- [What it does](#what-it-does)
- [Quick start](#quick-start)
- [Tailscale](#tailscale)
- [Run it yourself](#run-it-yourself)
- [Complete example](#complete-example)
- [How it is wired](#how-it-is-wired)
- [Configuration](#configuration)
- [API](#api)
- [Agent integrations](#agent-integrations)
- [Development](#development)
- [Security shape](#security-shape)
- [Project layout](#project-layout)
- [More docs](#more-docs)
- [License and notices](#license-and-notices)

## What it does

pr0xteus starts a short-lived Docker cell when a trusted caller needs a SOCKS5
exit. Each cell owns one WireGuard configuration, waits for a handshake, and
runs cellproxy — a first-party SOCKS5 proxy that also serves a control endpoint
with per-cell traffic metrics and a real liveness check. A cell is reaped once
it is idle (and carries no live connections) or unhealthy. Pools and country
routing are operator-owned local files; callers cannot name configs, images,
Docker arguments, or host paths.

It is not an open proxy. The control API needs a bearer token, binds to host
loopback in the supplied Compose stack, and reaches Docker only through a
restricted socket proxy. Caller input picks a configured country or pool — not
an image, a host path, or a provider config.

## Quick start

You need Linux, Docker, and a WireGuard `.conf` file from a VPN provider or
private network you are allowed to use. Docker already includes Compose, so
there is no separate Compose install dance.

### Install it

The installer works two ways. **Per-user** — no root, just for you:

```bash
curl -fsSL https://raw.githubusercontent.com/psyb0t/pr0xteus/main/install.sh | bash
```

That puts the `pr0xteus` command in `~/.local/bin/` and your config in
`~/.config/pr0xteus/` (owner-only). If `~/.local/bin` isn't on your `PATH` the installer
prints the exact one-liner to add it for bash or zsh.

**System-wide** — run it with `sudo` for one shared stack any docker-group user
can drive:

```bash
curl -fsSL https://raw.githubusercontent.com/psyb0t/pr0xteus/main/install.sh | sudo bash
```

That puts the command in `/usr/local/bin/` and the config in `/etc/pr0xteus/`
(root-owned, readable by the `docker` group). The mode is chosen from who runs
it — root → system-wide, otherwise per-user — and `--system` / `--user` force it.

Either way it drops the local `docker-compose.yml` and starter config, generates
a bearer token in an owner-only `.env`, and installs the `pr0xteus` command. No
source checkout required. It pins to the **latest tagged release** — never
`:latest` on your box — and the controller derives its matching cell image from
that tag, so both move together only when you upgrade.

Right after installing, edit the `.env` in your config directory if you want to
change the loopback ports, tune logging, or turn on the optional tailnet API
(below) — everything is a plain key you edit, not a CLI flag to remember.

Want to track `main` instead of a release? Add `--rolling` to force the moving
`:latest` image for a single run — on the installer
(`… | bash -s -- --rolling`) or on any `pr0xteus start` / `pr0xteus upgrade`.

### Give it one WireGuard exit

Copy your provider or private-network file into the config directory:

```bash
cp /wherever/you/keep/your-vpn.conf ~/.config/pr0xteus/secrets/wireguard/us.conf
```

Then edit these two small files so `us` means your file without `.conf` and
`US` is the country you want callers to request:

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

Start it:

```bash
pr0xteus start
```

The controller stays private on `http://127.0.0.1:8000`; metrics and health
stay on `http://127.0.0.1:9091`. Useful commands are deliberately boring:

```bash
pr0xteus status
pr0xteus logs --follow
pr0xteus stop
pr0xteus upgrade     # re-pin to the newest release, pull it, drop the old image
pr0xteus uninstall   # stop the stack, remove the command, ask before deleting data
```

`upgrade` re-pins `~/.config/pr0xteus/.env` to the latest release and removes the
previous image so dangling layers don't pile up; `uninstall` only deletes your
`~/.config/pr0xteus` data and volumes if you say yes at the prompt.

The [complete example](docs/complete-example.md) shows an allocation, a
private SOCKS5 consumer, and an actual egress proof.

## Tailscale

Want the same private API from another machine without opening a host port?
Give the installed stack its own Tailscale identity. Set these in
`~/.config/pr0xteus/.env`, then run `pr0xteus start`:

```dotenv
PR0XTEUS_TAILSCALE_ENABLED=true
TS_AUTHKEY=tskey-auth-xxxx        # reusable or ephemeral auth key
TS_HOSTNAME=pr0xteus              # tailnet machine name
TS_EXTRA_ARGS=--accept-dns=false  # extra `tailscale up` flags (see below)
```

That starts an optional sidecar in its own network namespace, waits for it to
join your tailnet, and configures Tailscale Serve to proxy
`http://pr0xteus/v1/...` to the private controller. It exposes no host port,
does not touch a host Tailscale client, and still requires the bearer token.
The sidecar's tailnet state lives in `~/.config/pr0xteus/tailscale/state`, so it keeps
the same identity across restarts.

**Those three values are the only ones you set.** The compose file fixes the
rest for *kernel-mode* Tailscale — `TS_USERSPACE=false` with `NET_ADMIN`,
`NET_RAW`, and `/dev/net/tun` — so the sidecar runs a real `tailscale0`
interface and outbound traffic to `100.64.0.0/10` uses the sidecar's own tailnet
identity, not the host's. `TS_STATE_DIR` is likewise fixed to the bind-mounted
state dir above.

**`TS_EXTRA_ARGS` is passed verbatim to `tailscale up`,** which is where every
other option goes — the sidecar image has no dedicated env var for them:

- **Headscale (self-hosted control server)** — point it at your server and use a
  Headscale-issued pre-auth key:
  ```dotenv
  TS_AUTHKEY=<headscale-preauthkey>
  TS_EXTRA_ARGS=--login-server=https://headscale.example.com --accept-dns=false
  ```
- **Tags / ACLs** — `--advertise-tags=tag:proxy`.
- **Ephemeral node** — issue an ephemeral auth key; it deregisters on stop.

Run `tailscale up --help` for the full flag set.

## Run it yourself

The wrapper is the normal operator path. If you want every Docker command in
front of you, initialize the same local stack directly:

```bash
mkdir -p ~/.config/pr0xteus
docker run --rm --user "$(id -u):$(id -g)" \
  -v "$HOME/.config/pr0xteus:/config" \
  psyb0t/pr0xteus:latest config init \
  --config-dir /config \
  --host-config-dir "$HOME/.config/pr0xteus" \
  --controller-image psyb0t/pr0xteus:latest
```

Add your WireGuard file and edit the generated pool and routing files exactly
as in [Quick start](#quick-start). Validate and start it with Docker itself:

```bash
docker run --rm --user "$(id -u):$(id -g)" \
  -v "$HOME/.config/pr0xteus:/config:ro" \
  psyb0t/pr0xteus:latest config check --config-dir /config

docker compose --project-directory "$HOME/.config/pr0xteus" \
  --env-file "$HOME/.config/pr0xteus/.env" \
  -f "$HOME/.config/pr0xteus/docker-compose.yml" \
  up --detach --pull always
```

That local `docker-compose.yml` is generated by the image and is yours to
inspect or run directly. The wrapper is only a small guardrail around these
same commands. [Deployment details](docs/deploy.md) include the direct
Tailscale command too.

## Complete example

The [operator walkthrough](docs/complete-example.md) starts with the installer,
maps a real WireGuard file into a logical country pool, starts the private
stack, then proves a transient consumer actually exits through its returned
SOCKS5 URL. It also covers replacing a bad allocation and why a health check
is not a live-tunnel check.

## How it is wired

```text
trusted client ── private HTTP API ── pr0xteus ── socket proxy ── Docker
                                      │
                                      └── private SOCKS5 cell ── WireGuard peer
```

- **Controller** — validates requests, selects an approved pool config, starts
  and monitors cells, and exposes Prometheus metrics.
- **Socket proxy** — has the raw Docker socket; the controller gets only the
  required container API surface.
- **Cell** — gets `NET_ADMIN`, `SETUID`, `SETGID`, `/dev/net/tun`, and one
  read-only config file; it starts as root for network setup and then runs the
  SOCKS5 daemon as UID 1500.

The fuller story is in [docs/architecture.md](docs/architecture.md), with
implementation detail in [internal/README.md](internal/README.md) and
[cell/README.md](cell/README.md).

## Configuration

All sensitive or provider-specific material lives under `~/.config/pr0xteus/`, stays
out of Git, and stays out of Docker build contexts:

- `secrets/wireguard/*.conf` — real WireGuard files.
- `secrets/pools.yaml` — named pools and their approved config basenames.
- `config/egress-routing.yaml` — country-to-pool policy.
- `.env` — the private bearer token, absolute host configuration path, and image selection.

The installer writes `.env`; it is not something you need to create by hand.
A published controller already names its matching cell: `latest` uses
`cell-latest`, while `vX.Y.Z` uses `cell-vX.Y.Z`. An operator can override
`PR0XTEUS_CELL_IMAGE`, but that override must be pinned by digest. Local
development uses the locally built `psyb0t/pr0xteus:cell-dev` with the explicit
unpinned escape hatch in the generated `.env`.

To stay on a particular release, change
`PR0XTEUS_CONTROLLER_IMAGE=psyb0t/pr0xteus:vX.Y.Z` in
`~/.config/pr0xteus/.env`, run `pr0xteus setup`, then run `pr0xteus start`.

Pool filenames need not be provider-specific. For a file that does not follow
the legacy `<country>-<location>.conf` convention, add its country explicitly:

```yaml
pools:
  primary:
    configs: [my-provider-node]
    exit_countries:
      my-provider-node: US
```

## API

The API is versioned and JSON-only:

```text
POST   /v1/proxies             # request exactly one configured country or pool
GET    /v1/pools               # authenticated pool/tunnel operator view
GET    /v1/cells               # live cells with per-cell traffic metrics
GET    /v1/cells/{containerID} # one cell, including its traffic snapshot
DELETE /v1/cells/{containerID} # destroy a cell on demand
GET    /healthz                # separate metrics listener, keep it private
GET    /metrics                # Prometheus, separate metrics listener
```

`POST /v1/proxies` returns a private `socks5://` URL only after the cell has
completed its WireGuard handshake wait and opened its SOCKS5 listener.

`GET /v1/cells` is the observability view: cells are discovered straight from
Docker (by a `pr0xteus.parent.id` label — no in-memory registry to drift), and
each carries the live traffic snapshot scraped from its cellproxy control port:

```console
$ curl -sH "Authorization: Bearer $TOKEN" http://127.0.0.1:8000/v1/cells
{
  "cells": [
    {
      "containerId": "9f3c1a2b4d5e", "pool": "primary", "state": "running",
      "traffic": {
        "requests": 42, "bytesUp": 18234, "bytesDown": 918273, "active": 3,
        "destinations": [
          { "destination": "example.com:443", "requests": 40, "bytesDown": 900000 }
        ]
      }
    }
  ]
}
```

`GET /v1/cells/{id}` inspects one cell and `DELETE /v1/cells/{id}` destroys it on
demand. The exact request, response, and failure contract live in
[docs/api.md](docs/api.md).

## Agent integrations

This repo ships a documentation skill for agents that need to drive a trusted
pr0xteus controller. It knows the private control API, the real setup
sequence, and the sharp edge around private Docker networking. It does **not**
pretend pr0xteus is an MCP server, because it is not one.

### Claude Code

```bash
claude plugin marketplace add psyb0t/agents
claude plugin install pr0xteus@psyb0t
```

Claude Code asks for the private controller URL and bearer token when the
plugin is enabled; the token is stored as sensitive user configuration.

### Codex

```bash
codex plugin marketplace add psyb0t/agents
codex plugin add pr0xteus@psyb0t
```

Inside this repository, use `$pr0xteus`. After marketplace installation, use
`$pr0xteus:pr0xteus`.

### OpenClaw

The same documentation skill is published to ClawHub on tagged releases:

```bash
openclaw skills install @psyb0t/pr0xteus
```

There is intentionally no OpenClaw MCP bridge: pr0xteus exposes a private HTTP
API, not an MCP endpoint. The detailed setup reference is
[here](.agents/skills/pr0xteus/references/setup.md).

## Development

Everything supported goes through Make. Go tooling, formatting, linting, and
tests run inside `Dockerfile.dev`, not through a host Go installation.

Source checkout and Make are for development only; an operator uses the image
and the installer quick start above.

```bash
make help          # every supported operation
make format        # gofumpt + shfmt
make lint          # Go, shell, and format checks
make test          # unit tests plus a real Testcontainers WireGuard/SOCKS5 stack
make test-api      # build pr0xteus from its Dockerfile in Testcontainers, hit every route
make test-real     # opt-in real Surfshark allocation and public-IP egress proof
make test-coverage # gate every package at 90% (servicepack coverage engine)
make audit         # govulncheck
make audit-compose # Compose safety checks
make build         # controller image
make build-cell    # WireGuard + cellproxy image
```

`make test-api` (and the broader `make test-integration`) use Testcontainers to
build and start the **production** controller image, a self-contained WireGuard
peer container (built from
[`tests/testinfra/wireguard/`](tests/testinfra/wireguard/)), the cell image, and
a sibling SOCKS5 client on an isolated Docker network. The
API test drives every control-plane route over real HTTP and proves that SOCKS5
traffic traverses the WireGuard tunnel to a private test HTTP server on the peer.
It needs no provider account, real WireGuard bundle, host port, or persistent
container, and Testcontainers tears down only the exact resources it created.

`make test-coverage` runs the servicepack coverage engine over
`go test -tags=integration ./...` with `-coverpkg=<module>/...`, so **every**
package is gated at 90% — the controller service included, via native coverage
data merged from the real controller container (test runs swap in a `-cover`
image; normal `test-api`/`test-integration` runs use the production `Dockerfile`).
It excludes only non-hand-written code under test: `cmd/` mains, the `tests/`
harness, generated code, and mocks.

`make test-real` is deliberately separate from `make test` and CI. It loads
the ignored local Surfshark bundle at `secrets/wg/surfshark-wireguard/` with
the matching `secrets/wg/pools.yaml` and `config/egress-routing.yaml`, starts
its own Testcontainers controller and consumer, requests a real egress proxy,
then verifies that the consumer's public IPv4 address changes when traffic
uses the returned SOCKS5 URL. Set `PR0XTEUS_REAL_TEST_COUNTRY=US` before
running `make test-real` to select the routing input. It creates a fresh test
token and a unique controller scope; it neither reads the production token nor
touches a running stack.

The public Go client surface is under [`pkg/client`](pkg/client); it is useful
when another Go service should request and retry egress proxies without
re-implementing the HTTP contract.

## Security shape

- The raw Docker socket is only mounted into `docker-socket-proxy`, never the
  controller or cells.
- The controller is non-root, read-only, capability-empty, resource-capped,
  log-capped, and exposes only loopback ports.
- Optional tailnet access is a separate, capability-minimized Tailscale
  sidecar. It is the only service with `/dev/net/tun`, `NET_ADMIN`, and
  `NET_RAW`; it has its own tailnet identity and exposes only the authenticated
  controller API through Tailscale Serve.
- A cell has the specific WireGuard exception: `NET_ADMIN` and `/dev/net/tun`,
  plus `SETUID`/`SETGID` solely for its one-way final drop to the non-root
  cellproxy account. It begins with default-drop firewall policy, allows the
  WireGuard peer, and does not start cellproxy until a handshake arrives. The
  cellproxy control server is opened only on the internal cell network, never on
  the WireGuard egress side.
- Real configuration and tokens are neither tracked nor included in either
  image build context.
- The controller accepts a strict, size-bounded JSON body and stores only a
  SHA-256 digest of the bearer token after startup.
- Every cell carries a controller scope label, so shutdown and orphan recovery
  only ever act on cells from that controller scope.

These are meaningful boundaries, not magic. Anyone able to modify local pool
policy, read the bearer token, or control Docker on the host is already inside
the operator trust boundary.

## Project layout

```text
cmd/        — process entry point
internal/   — API, pool manager, Docker spawner, reaper, metrics
pkg/client/ — public Go client for the private control API
tests/      — Testcontainers-backed controller, WireGuard, cell, and SOCKS5 tests
cell/       — WireGuard + cellproxy worker image and entrypoint
docs/       — architecture, deployment, and API references
scripts/    — Makefile-backed dependency and image helpers
```

## More docs

- [Architecture](docs/architecture.md)
- [Complete setup and egress proof](docs/complete-example.md)
- [Deployment guide](docs/deploy.md)
- [Control API](docs/api.md)
- [Internal control-plane detail](internal/README.md)
- [Cell boot and firewall detail](cell/README.md)
- [Changelog](CHANGELOG.md)
- [Third-party notices](THIRD_PARTY_NOTICES.md)

## License and notices

The project source is [MIT licensed](LICENSE). See
[THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md) and `vendor/` for dependency
licenses and attribution, including the development-only GPL-3.0 linter.
