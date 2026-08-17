# Changelog

## v0.9.6 — 2026-08-17

- Fixed installed controller startup with the default owner-only config
  directory. The generated Compose stack now runs the controller as the
  installing operator's UID:GID, so it can read WireGuard pools and routing
  without making them world-readable. `setup` and `upgrade` refresh that value
  for existing installs.

## v0.9.5 — 2026-08-17

- Fixed an intermittent end-to-end cell-proxy test failure. The test now waits
  for the real control endpoint to observe completed SOCKS traffic before
  asserting its metrics, matching the asynchronous connection lifecycle.

## v0.9.4 — 2026-08-17

- Restored the controller startup hook that selects the exact release-paired
  cell image from its build version. A versioned controller again uses its
  matching `:cell-vX.Y.Z` image instead of leaving the cell selection unset.
- Updated to Servicepack v1.6.4. pr0xteus now uses the framework's shared
  Docker-in-Docker test runner instead of maintaining a duplicate one, while
  retaining the project-specific real-egress test variables. This fixes the
  same Make targets across a host, a container with the Docker socket, and CI.

## v0.9.3 — 2026-08-17

- Restored the 90% coverage gate after the v0.9.2 bootstrap helper split. The
  bootstrap file writer now has direct tests for create/preserve, runtime
  template refresh, and write-error behavior.

## v0.9.2 — 2026-08-17

- Fixed the release lint failure from v0.9.1. Configuration bootstrap and the
  `config init` command are split into focused helpers without changing their
  behavior.

## v0.9.1 — 2026-08-17

- Fixed the installed command on owner-only configuration directories. `start`
  and `restart` now validate `.env` as the configuration owner instead of
  failing with a misleading permission error before Compose starts.
- `setup` and `upgrade` now refresh the generated base and host-port Compose
  templates alongside `.env.example`, while preserving operator-owned `.env`,
  pool and routing policy, WireGuard files, and Tailscale state. This delivers
  new runtime wiring to an existing install without making users rebuild their
  configuration.

## v0.9.0 — 2026-08-17

**Breaking before 1.0:** host-port settings now use complete `HOST:PORT`
values: `PR0XTEUS_HTTP_HOST_PORT`, `PR0XTEUS_METRICS_HOST_PORT`, and
`PR0XTEUS_SOCKS_HOST_PORT`. If you customized the old `*_PORT` settings, move
each value to its matching new variable with the intended bind address. Also
set `PR0XTEUS_SOCKS_PUBLIC_ADDRESS` to the real address clients should receive
in lease URLs — never `0.0.0.0`.

- Added `PR0XTEUS_DISABLE_HOST_PORTS=false`. Set it to `true` to remove every
  controller, metrics, and SOCKS host binding, leaving a Tailscale sidecar or
  another private Docker-network gateway as the only route in.
- Added generated host-port and no-host-port Compose overlays. The installed
  command chooses one on every Compose call, so `pr0xteus upgrade` can add the
  new behavior without replacing the operator-owned base Compose file.
- Tailscale deployment docs now use `PR0XTEUS_DISABLE_HOST_PORTS=true` for an
  entirely internal controller-to-sidecar hop.

## v0.8.3 — 2026-08-17

- Made the Makefile development image fully vendored. It no longer makes an
  unused network `go mod download` during CI image builds, so test execution
  uses the repository's committed dependency tree.

## v0.8.2 — 2026-08-17

- Fixed local development and real-infrastructure tests: the locally built
  `:cell-dev` worker is now used as-is. Released controllers still pull their
  exact paired `:cell-vX.Y.Z` worker before allocating a tunnel.

## v0.8.1 — 2026-08-17

- Restored the Servicepack quality-target contract: removed the stale root
  recipes that Make was already overriding, so linting, testing, formatting,
  and image builds all use the framework implementation.
- The documented Servicepack coverage hook now builds the local cell image
  first, then delegates to the framework coverage runner. This keeps the real
  cell integration precondition without maintaining a second test workflow.
- Fixed the lint findings in the controller error paths, pool startup helpers,
  config validation test, and public proxy field tags.

## v0.8.0 — 2026-08-17

**Breaking before 1.0:** cells are now release-paired to their controller.
`PR0XTEUS_CELL_IMAGE`, `PR0XTEUS_BUILT_CELL_IMAGE`, and the unpinned-cell
development escape hatch are gone. Set `PR0XTEUS_CONTROLLER_IMAGE` to choose a
release; its controller selects and pulls exactly its matching cell image.

- Controller images now carry their own build version. `:latest` starts
  `:cell-latest`; `:vX.Y.Z` starts `:cell-vX.Y.Z`; local builds consistently
  use `:cell-dev`. The controller pulls the exact cell image before creating a
  tunnel, so a missing worker image produces a clear allocation failure instead
  of a later opaque Docker create error.
- Image publication builds the cell first, then the matching controller. The
  controller build receives its published tag as a build argument rather than
  relying on a runtime environment setting.
- Updated to Servicepack v1.6.0, which includes the application name, commit,
  and build version in the process scope used by structured logs.

## v0.7.1 — 2026-08-16

- Added `pr0xteus restart` to the installed command. It stops the stack and
  starts it again through the normal validation path.

## v0.7.0 — 2026-08-16

- **Control API collections are now consistent.** `GET /v1/proxies`,
  `GET /v1/pools`, and `GET /v1/cells` all accept `limit` and `offset` and
  return their items with `limit`, `offset`, and exact `total` fields.
- **Cell discovery errors now report correctly.** A Docker discovery failure
  returns the normal `500` error envelope for both the cell collection and a
  single-cell lookup; it no longer looks like an empty collection or a missing
  cell.
- Made every documented allocation call explicitly use `--request POST`, so it
  is obvious that `POST /v1/proxies` creates one lease while `GET /v1/proxies`
  lists active exits.

## v0.6.0 — 2026-08-16

**Breaking before 1.0:** `POST /v1/proxies` no longer returns a cell hostname
usable only inside Docker. It now returns a short-lived, credentialed
controller SOCKS5 URL. Pass the full returned URL to your SOCKS client (for
example, `curl --proxy "$url" …`); callers no longer need membership of the
cell egress network.

- Added a loopback-published, lease-authenticated controller SOCKS5 gateway.
  A random URL is bound to exactly one selected cell, expires after the
  configured TTL, accepts TCP CONNECT only, and sends destination DNS plus
  egress through that cell's WireGuard interface.
- Added `GET /v1/proxies`, a bounded, read-only active-proxy inventory with
  pool, cell state, exit metadata, last use, and the latest issued URL plus its
  expiry. It never allocates a new cell or credential.
- The controller now runs only on the internal cell-control network; it has no
  egress-network route or destination DNS resolution. Cells join that network
  alongside egress, and the controller gateway reaches their private SOCKS5
  listeners there.
- Added gateway and lease regression coverage, including invalid credentials,
  expiry, no controller-side DNS, real controller→cell proxying, and a
  real-provider test topology with a separate direct baseline client.
- Updated the image-first quick start, deployment guide, architecture, API
  reference, and agent skill for host or trusted-client SOCKS usage.

## v0.5.0 — 2026-08-16

Security + reliability hardening. No REST/MCP contract change and no config
migration; the `socks5://` URL from `POST /v1/proxies` is unchanged.

- **Security: the cell no longer accepts new inbound on the WireGuard side.**
  The cell firewall carried a blanket `-A INPUT -i wg0 -j ACCEPT` that left
  cellproxy's SOCKS5 port and its `/status` control endpoint reachable from the
  tunnel (egress) side — the VPN provider, and co-tenant peers on providers that
  don't isolate them. Return traffic for the proxy's own outbound flows is
  already covered by the ESTABLISHED,RELATED accept, so nothing legitimate
  relied on it; new inbound is now accepted only on the cell's docker network.
- **Fix: the orphan reaper no longer reaps a cell mid-spawn.** The reconciler
  protected only cells already published to a pool, so a spawn in progress
  (running, not yet tracked) could be killed and surface as a spurious 503. The
  ticker path now leaves containers younger than the spawn deadline alone; boot
  reaping still clears everything a prior process left behind.
- **Fix: data race on a freshly-spawned tunnel.** The returned acquisition copy
  is taken before the tunnel is published, so a concurrent acquire can no longer
  mutate the struct while it is being read (a `-race` finding).
- **Prometheus tunnel-pool metrics now emit.** `spawns_total`, `reaps_total`,
  `acquires_total`, `hot_tunnels`, and `spawn_seconds` were registered but never
  updated; they now record spawn outcome + duration, reaps by reason, successful
  acquisitions, and the per-pool hot-tunnel gauge.
- **Client: an exhausted pool fails fast.** `pkg/client` maps the server's 503
  pool-unavailable response to `ErrPoolExhausted`, so the documented terminal
  path fires instead of retrying the full backoff.
- Docs: rewrote `docs/architecture.md` with the real control/data-plane design
  (it was framework boilerplate), removed a stale getting-started page,
  documented the `/v1/cells` surface in the agent skill, hardened the installer
  `uninstall` path resolution, and corrected the `make build` vs `docker-build`
  and `config-init` notes.

## v0.4.0 — 2026-08-16

- **Installer: per-user and system-wide modes.** `install.sh` now installs
  either per-user (no root → `~/.local/bin` + `~/.config/pr0xteus`) or
  system-wide (sudo → `/usr/local/bin` + `/etc/pr0xteus`, root-owned and
  readable by the `docker` group so any docker-group operator drives the one
  shared stack). The mode auto-detects from `EUID`, with `--user` / `--system`
  to force it; `--rolling` still pins the moving `:latest`. A per-user install
  that finds `~/.local/bin` off `PATH` prints the exact bash/zsh one-liner to
  add it.
- **Breaking: per-user config moved to `~/.config/pr0xteus`** (XDG,
  `$XDG_CONFIG_HOME`-aware) from `~/.pr0xteus`. Re-running the installer writes
  the new location; an existing `~/.pr0xteus` is left untouched — move it, or
  set `PR0XTEUS_HOME=~/.pr0xteus` to keep using it.
- **Agent skill install-safety.** The `.agents/` skill now tells the model to
  download and inspect `install.sh` before running it (never `curl | bash`),
  documents both install modes plus a direct `config init` + `docker compose`
  path, and uses the new config paths.
- Test coverage now runs through the servicepack v1.5.0 engine, gating every
  package (controller services included) instead of a custom subset. Added
  `make test-api`, which builds the image from its Dockerfile in Testcontainers
  and drives every control-plane route over real HTTP.

## v0.3.0 — 2026-08-14

- Replaced microSocks in the cell with `cellproxy`, a first-party Go SOCKS5
  proxy built from this repo's own source. It records per-cell traffic — request
  counts, bytes up/down, live-connection count, dial failures, and a bounded
  byte-ranked destination breakdown — and serves them, plus a real liveness
  check, on an internal control HTTP port reachable only over the cell network.
  The public `socks5://` contract from `POST /v1/proxies` is unchanged.
- Each cell now records its parent controller (a `pr0xteus.parent.id` label plus
  a `PR0XTEUS_PARENT_ID` env var), so the controller rediscovers its children by
  querying Docker rather than trusting only in-memory state.
- The reaper now probes each cell's real `cellproxy` `/healthz` instead of a
  handshake-age timer, and idle-reap is session-aware: a cell still carrying live
  connections is not killed underneath its callers.
- Added the cell observability API: `GET /v1/cells` (every live cell with its
  traffic snapshot), `GET /v1/cells/{containerID}` (one cell), and
  `DELETE /v1/cells/{containerID}` (destroy a cell on demand). Routes remain
  hand-wired on `aichteeteapee`.
- New config: `PR0XTEUS_CELL_CONTROL_PORT` (default `9090`) and the auto-derived
  `PR0XTEUS_PARENT_ID`.

## v0.2.3 — 2026-08-14

- Bumped the alpine base from 3.20.6 to 3.24.1 across the controller, cell, and
  integration images, regenerating the pinned apk versions for the 3.24 branch.
- Updated Go dependencies: `testcontainers-go` 0.43.0 → 0.44.0,
  `moby/moby/api` 1.54.2 → 1.55.0, and `prometheus/client_golang`
  1.23.2 → 1.24.1.

## v0.2.2 — 2026-08-14

- Fixed the cell image build so the published multi-architecture image ships:
  `cell/Dockerfile` copied `entrypoint.sh` relative to the wrong directory, but
  the image is built with the repository root as its context, so the copy failed.
  It now copies `cell/entrypoint.sh`, and the local build uses the same root
  context as the published build.
- Added unit tests for config-bootstrap error paths, API country-code
  validation, and the client retry / dispatch / backoff helpers so the suite
  clears the 90% coverage floor.

## v0.2.1 — 2026-08-14

- The installer now pins the local stack to the latest tagged release instead of
  `:latest`, writing that exact tag into `~/.pr0xteus/.env`; the controller
  derives its matching `cell-<tag>` image, so both move only on an explicit
  upgrade. Pass `--rolling` to the installer or to `start` / `upgrade` to use the
  moving `:latest` image for a single run.
- Added `pr0xteus upgrade` (re-pin to the newest release, pull it, drop the
  previous image) and `pr0xteus uninstall` (stop the stack, remove the command,
  and delete `~/.pr0xteus` and its volumes only after a prompt). Installer and
  wrapper output is now plain human-readable progress rather than JSON log lines.
- Ignored `.backup` and `scripts/.post-update-temp` so a Servicepack update's
  backup archive and scratch directory can never be committed.

## v0.2.0 — 2026-08-14

- Made the published Docker image the operator path: `pr0xteus config init`
  creates the ignored local skeleton and `pr0xteus config check` validates it;
  `docker-compose.yml` starts the released controller and matching cell image.
- **Breaking.** Replaced `PR0XTEUS_API_TOKEN_FILE` and Docker Compose secrets
  with the required `PR0XTEUS_API_TOKEN` in the owner-only, ignored `.env`.
  Run `pr0xteus config init` in the deployment directory, then keep the
  generated token in that `.env`.
- Kept source checkout and Makefile commands development-only, and preserved
  pr0xteus's custom `.golangci.yml` during future Servicepack updates.

## v0.1.2 — 2026-08-14

- Fixed vendoring so Go can resolve Moby's `api/types/build` package and
  Testcontainers' `internal/config` package in CI instead of losing them to
  broad root-output ignore rules.
- Pinned the reusable Go workflow to the repository's required Go 1.26.6
  toolchain and granted the collaborator gate its required issue and pull
  request permissions.

## v0.1.1 — 2026-08-14

- Granted each reusable-workflow caller the minimum token permissions its
  called jobs require, fixing GitHub Actions startup failures for CI, releases,
  mirrors, archives, badges, Docker publication, and ClawHub publication.

## v0.1.0 — 2026-08-14

- Extracted pr0xteus into its own reusable Go and Docker project.
- Hardened the private controller, Docker socket boundary, WireGuard cell, and
  local configuration workflow.
- Added a complete local setup, allocation, and private SOCKS5 egress proof
  guide, plus the matching Claude Code/Codex documentation skill.
- CI now reports merged unit and Testcontainers controller coverage, publishes
  both multi-architecture images, syncs Docker Hub metadata, cuts tag releases,
  and publishes the documentation skill to ClawHub on tagged releases.
- Published controller images now carry their matching cell reference
  (`latest` -> `cell-latest`, `vX.Y.Z` -> `cell-vX.Y.Z`), while operators can
  still override it with an immutable digest.
- Added the Docker Compose plugin to the development image so `make
  config-check` actually validates the rendered stack.
- Existing local `.env` files that name `psyb0t/pr0xteus-cell:dev` now get a
  non-destructive migration warning from `make config-init`.
