# pr0xteus

[![CI](https://github.com/psyb0t/pr0xteus/actions/workflows/pipeline.yml/badge.svg?branch=main)](https://github.com/psyb0t/pr0xteus/actions/workflows/pipeline.yml)
[![version](https://raw.githubusercontent.com/psyb0t/pr0xteus/badges/version.svg)](https://github.com/psyb0t/pr0xteus/releases)
[![license](https://raw.githubusercontent.com/psyb0t/pr0xteus/badges/license.svg)](LICENSE)
[![coverage](https://raw.githubusercontent.com/psyb0t/pr0xteus/badges/coverage.svg)](https://github.com/psyb0t/pr0xteus/actions/workflows/pipeline.yml)
[![Docker Pulls](https://img.shields.io/docker/pulls/psyb0t/pr0xteus?style=flat-square)](https://hub.docker.com/r/psyb0t/pr0xteus)

WireGuard-backed SOCKS5 tunnel pools for services that need a configured exit
without turning your Docker host into an open proxy.

You keep the WireGuard files, decide which countries and pools exist, and keep
the controller private. A trusted workload asks for one approved route; it
gets a private SOCKS5 cell only after the WireGuard handshake is up.

## Contents

- [What it does](#what-it-does)
- [Quick start](#quick-start)
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
exit. Each cell owns one WireGuard configuration, waits for a handshake, runs
microSocks, and is reaped once it is idle or unhealthy. Pools and country
routing are operator-owned local files; callers cannot name configs, images,
Docker arguments, or host paths.

It is not an open proxy. The control API needs a bearer token, binds to host
loopback in the supplied Compose stack, and reaches Docker only through a
restricted socket proxy. Caller input picks a configured country or pool — not
an image, a host path, or a provider config.

## Quick start

You need Linux, Docker Engine, Docker Compose v2, Make, and a real WireGuard
configuration bundle from a provider or network you are authorized to use.

```bash
git clone https://github.com/psyb0t/pr0xteus.git
cd pr0xteus
make config-init
```

`config-init` preserves existing local configuration. On a new checkout it
puts editable ignored skeletons at `secrets/pools.yaml` and
`config/egress-routing.yaml`, creates an ignored token and `.env`, and tells
you where to put your `*.conf` files.

Put real WireGuard files under `secrets/wireguard/`, replace `example-node` in
`secrets/pools.yaml` with their basenames, then run:

```bash
make config-check
make run
```

`make run` starts persistent Compose services. The controller is then private
on `http://127.0.0.1:8000`; metrics and health live on
`http://127.0.0.1:9091`. The
[complete example](docs/complete-example.md) shows the exact config, a real
allocation, and a private SOCKS5 egress proof.

## Complete example

The [operator walkthrough](docs/complete-example.md) starts with an empty
checkout, maps a real WireGuard file into a logical country pool, explains the
absolute-host-path gotcha, starts the private stack, then proves a transient
consumer actually exits through its returned SOCKS5 URL. It also covers
replacing a bad allocation and why a health check is not a live-tunnel check.

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

All sensitive or provider-specific material is ignored by Git and excluded
from Docker build contexts:

- `secrets/wireguard/*.conf` — real WireGuard files.
- `secrets/pools.yaml` — named pools and their approved config basenames.
- `config/egress-routing.yaml` — country-to-pool policy.
- `secrets/pr0xteus_api_token` — private bearer token.
- `.env` — absolute host paths and image selection.

Start from [.env.example](.env.example). A published controller already names
its matching cell: `latest` uses `cell-latest`, while `vX.Y.Z` uses
`cell-vX.Y.Z`. An operator can override `PR0XTEUS_CELL_IMAGE`, but that
override must be pinned by digest. Local development uses the locally built
`psyb0t/pr0xteus:cell-dev` with the explicit unpinned escape hatch in the
generated `.env`.

An existing local `.env` created before this image split still says
`psyb0t/pr0xteus-cell:dev`; change only that value to
`psyb0t/pr0xteus:cell-dev`. `make config-init` warns but does not overwrite
operator-owned configuration.

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
POST /v1/proxies  # request exactly one configured country or pool
GET  /v1/pools    # authenticated pool/tunnel operator view
GET  /healthz     # separate metrics listener, keep it private
GET  /metrics     # Prometheus, separate metrics listener
```

`POST /v1/proxies` returns a private `socks5://` URL only after the cell has
completed its WireGuard handshake wait and opened its SOCKS5 listener. The
exact request, response, and failure contract live in [docs/api.md](docs/api.md).

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

```bash
make help          # every supported operation
make format        # gofumpt + shfmt
make lint          # Go, shell, and format checks
make test          # unit tests plus a real Testcontainers WireGuard/SOCKS5 stack
make test-real     # opt-in real Surfshark allocation and public-IP egress proof
make test-coverage # runs all test packages and requires 90% production coverage
make audit         # govulncheck
make audit-compose # Compose safety checks
make build         # controller image
make build-cell    # WireGuard + microSocks image
```

`make test-integration` uses Testcontainers to build and start the production
controller, a real WireGuard peer, the production cell image, and a sibling
SOCKS5 client on an isolated Docker network. It allocates a proxy through the
real API and proves that SOCKS5 traffic traverses the WireGuard tunnel to a
private test HTTP server. It needs no provider account, real WireGuard bundle,
host port, or persistent container; Testcontainers tears down only the exact
resources it created.

`make test-coverage` runs `go test -tags=integration -race ./...`, so it
executes unit tests and every Testcontainers package, including `tests/*_test.go`.
Its 90% gate measures pr0xteus production code only
(`internal/pkg/services/pr0xteus` and `pkg/client`): Go does execute test
source, but does not treat `_test.go` files as coverable production code. The
fixture exports native coverage from its test-only controller image into
ignored `.cover/` storage and merges it with the test profile, so the reported
number includes the real controller process instead of only test binaries.
Normal integration runs keep using `Dockerfile`, the production controller
image.

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
- A cell has the specific WireGuard exception: `NET_ADMIN` and `/dev/net/tun`,
  plus `SETUID`/`SETGID` solely for its one-way final drop to the non-root
  microSocks account. It begins with default-drop firewall policy, allows the
  WireGuard peer, and does not start microSocks until a handshake arrives.
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
cell/       — WireGuard + microSocks worker image and entrypoint
examples/   — safe templates copied into ignored local configuration
docs/       — architecture, deployment, and API references
scripts/    — Makefile-backed setup, dependency, and image helpers
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
