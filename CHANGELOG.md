# Changelog

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
