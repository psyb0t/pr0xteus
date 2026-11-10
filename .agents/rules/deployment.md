# Deployment and images

Prefix: DEP. Operators run the published image through `install.sh` and the `pr0xteus` wrapper; the source checkout is for development only.

- **DEP1:** `docker-compose.yml`, `docker-compose.host-ports.yml`, `docker-compose.no-host-ports.yml` and `.env.example` at the root must stay byte-identical to `internal/pkg/services/pr0xteus/bootstrap/`. The `TestRoot*MatchesBootstrapTemplate` tests in `bootstrap_test.go` fail otherwise. Edit both copies together.
- **DEP2:** `config init` creates operator files only when they are missing, refreshes `.env.example` and, with `--refresh-runtime-templates`, the managed Compose files. It never overwrites `.env`, `pools.yaml`, `egress-routing.yaml`, WireGuard files or Tailscale state. Keep that contract in `bootstrap.go`, `install.sh` and `scripts/pr0xteus.sh`.
- **DEP3:** The controller picks its cell image from its own build version (`cmd/init.go` calls `ConfigureCellImageVersion(buildVersion)`; `cellImageForVersion` returns `psyb0t/pr0xteus:cell-<version>`). There is no environment override. The CI build sets `BUILD_VERSION` so the published controller always names a cell tag built from the same commit; change the pipeline and this pairing together.
- **DEP4:** The controller reads `TUNNEL_POOL_*` and `PR0XTEUS_*` variables (`config.go`). Operators set the `PR0XTEUS_*` values in `.env`, and `docker-compose.yml` maps them onto the controller environment. A new setting gets a `.env.example` line when operators should change it, and a row in `docs/configuration.md` either way.
- **DEP5:** `install.sh` downloads `scripts/pr0xteus.sh` from `main`, and `pr0xteus upgrade` downloads it from the release tag it pins (from `main` with `--rolling`). A wrapper change reaches new installs as soon as it lands on `main`, before any release. `scripts/test-wrapper-upgrade.sh` runs inside `make test-coverage`; extend it for wrapper upgrade behavior.
- **DEP6:** `make audit-compose` must pass after any Compose change.
