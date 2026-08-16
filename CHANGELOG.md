# Changelog

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
  hand-wired on `aichteeteapee`; see [ADR 0001](docs/adr/0001-cell-metrics-proxy-and-control-api.md).
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
