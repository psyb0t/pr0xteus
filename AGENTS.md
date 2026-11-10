# pr0xteus

Instructions for coding agents and contributors changing this repository. Operators who only run pr0xteus want the documentation site at https://psyb0t.github.io/pr0xteus/ instead.

## Agent setup

`CLAUDE.md` is a committed symlink to `AGENTS.md`, and `.claude` is a committed symlink to `.agents`, so Claude Code reads the same instructions, rules and skills as Codex. Claude's Edit and Write tools refuse to write through a symlink, so edit `AGENTS.md` and `.agents/rules/*.md` directly.

## What it is

pr0xteus hands short-lived SOCKS5 and HTTP proxy URLs to trusted clients. Each URL routes through a WireGuard tunnel from an operator-approved pool. The controller image is `psyb0t/pr0xteus`. It starts one Docker "cell" per pool on demand, each cell holding one WireGuard configuration, and removes idle or unhealthy cells. Callers never see provider configuration, and the host never becomes a VPN client.

The Go service is built on [servicepack](https://github.com/psyb0t/servicepack), a clone-and-own Go service framework. The one project service is `internal/pkg/services/pr0xteus`.

## Architecture

- Controller (`internal/pkg/services/pr0xteus`): one servicepack service. `New` in `service.go` loads `Config`, `pools.yaml`, `egress-routing.yaml` and the Docker client, and fails before touching Docker when any of them is invalid. `Run` reaps orphaned cells, starts the reaper, then serves four listeners: the control API (`:8000`), the SOCKS5 gateway (`:1080`), the HTTP proxy gateway (`:8080`) and the unauthenticated metrics listener (`:9091`, `/metrics` and `/healthz`).
- `APIServer` (`api.go`, `cells.go`): bearer-authenticated `/v1` routes. It keeps only the SHA-256 digest of the token and compares digests in constant time.
- `Manager` (`pool.go`, `routing.go`): pool selection, the single hot tunnel per pool, the recent-failure cache, fallback pools and country routing.
- `leaseRegistry` (`lease.go`): issues the random lease credentials in returned URLs and stores only a digest of each secret. Both URLs of one lease route to the same cell.
- `ProxyGateway` (`proxy_gateway.go`) and `HTTPProxyGateway` (`http_proxy_gateway.go`): accept lease credentials and relay every destination connection to the selected cell's private SOCKS5 listener. The controller never resolves or dials a destination itself.
- `CellSpawner` (`spawner.go`): creates, probes, lists and kills cells through the Docker API, which production reaches through `docker-socket-proxy`. It labels every cell (`pr0xteus.managed`, `pr0xteus.pool`, `pr0xteus.conf`, `pr0xteus.scope`, `pr0xteus.parent.id`).
- `Reaper` (`reaper.go`): idle, health and orphan loops.
- Cell (`cell/`, `cmd/cellproxy`, `internal/pkg/cellproxy`): `cell/entrypoint.sh` brings up `wg0` behind a default-drop firewall, waits for a handshake, then runs `cellproxy` as UID 1500. `cellproxy` serves SOCKS5 plus a control HTTP server (`/healthz`, `/status`) with per-destination traffic counters.
- Deployment: `install.sh` installs `scripts/pr0xteus.sh` as the `pr0xteus` command. That wrapper runs the image's `config init` and `config check` commands (`cmd/commands.go`, `bootstrap.go`) and drives `docker compose` with the generated files.

Data flow: client -> controller SOCKS5 or HTTP gateway (lease check) -> cell SOCKS5 over the internal `pr0xteus-cell-control` network -> WireGuard -> destination. The control API path is client -> bearer auth -> `APIServer` -> `Manager` -> `CellSpawner` -> socket proxy -> Docker.

## Stack

- Go 1.26.10 in `Dockerfile`, `Dockerfile.dev` and `cell/Dockerfile` (`go 1.26.6` minimum in `go.mod`), `CGO_ENABLED=0`, vendored modules.
- servicepack with `ctxerrors`, `ctxscope`, `gonfiguration`, `slogging`, aichteeteapee for HTTP, `github.com/moby/moby` for the Docker API, `things-go/go-socks5`, Prometheus `client_golang`, Cobra.
- Tests: testify and Testcontainers, with a WireGuard peer image in `tests/testinfra/wireguard/`.
- Images: digest-pinned Alpine runtimes. The controller runs as UID 1500. The cell starts as root and drops to UID 1500.
- Deploy: `docker-compose.yml` with `docker-socket-proxy`, the controller, an optional Tailscale sidecar and an inert network anchor.

## Layout

| Path | What lives there |
| --- | --- |
| `cmd/` | `main.go` (framework), `init.go` (pairs the cell image with `buildVersion`), `commands.go` (`config init`, `config check`), `cellproxy/` (the cell binary) |
| `internal/pkg/services/pr0xteus/` | The controller. `bootstrap/` holds the embedded templates written by `config init` |
| `internal/pkg/cellproxy/` | SOCKS5 server, connection accounting and the traffic recorder used inside cells |
| `pkg/client/` | Public Go client: lease requests, the retry staircase, VPN-only and public-first modes, preflight IP check |
| `cell/` | Cell `Dockerfile` and `entrypoint.sh` |
| `tests/controlapi/`, `tests/real/`, `tests/testinfra/` | Testcontainers suites (`integration` build tag; `real` adds the opt-in provider test) and their harness |
| `scripts/` | `pr0xteus.sh` (operator wrapper), image build scripts, `audit-compose.sh`, wrapper and lifecycle test scripts, `make/` overrides |
| `docker-compose*.yml`, `.env.example` | Must stay byte-identical to the `bootstrap/` templates (tests enforce it) |
| `examples/` | Sample `pools.yaml` and `egress-routing.yaml` |
| `docs/` | Documentation site, published by GitHub Pages from `main` at https://psyb0t.github.io/pr0xteus/ |
| `.agents/` | The `pr0xteus` skill, the Claude and Codex plugin manifests, and these rules |

## Commands

Docker, Make and Bash are enough. Every Go and shell tool runs in the `pr0xteus-dev` image.

```bash
make help            # every target
make lint            # shfmt, ShellCheck, go fix diff, golangci-lint
make lint-fix        # apply supported fixes; review the diff
make format          # gofumpt and shfmt
make test            # race-enabled go test ./..., Docker socket available
make test-unit       # the same without the Docker socket
make test-coverage   # builds the cell, runs unit and integration tests, 90% floor, wrapper upgrade tests
make test-api        # production controller image against a local WireGuard peer
make test-real       # opt-in real provider egress test (needs ignored secrets)
make test-installed  # drives a real installer stack; local only
make sec             # govulncheck and semgrep, writes sec.sarif
make generate        # go generate; CI fails on drift
make build           # static binary in build/
make build-cell      # psyb0t/pr0xteus:cell-dev
make audit-compose   # checks docker-compose.yml against the hardened floor
make docs            # builds the documentation site into .testing/docs-site
make run             # local images through the real installer and wrapper
```

`pre-commit.sh` runs `make lint && make test-coverage`. CI (`.github/workflows/pipeline.yml`) runs lint, `test-coverage`, `sec` and `generate` on every branch push.

## Rules

Read `.agents/rules/` before editing. One file per topic: framework ownership, security, API, errors, logging and metrics, deployment, testing, docs and the agent skill.
