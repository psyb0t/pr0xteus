# Servicepack framework

Prefix: FW. pr0xteus is a servicepack downstream. `make servicepack-update` rsyncs the framework over this tree, so where a change goes decides whether it survives the next update.

- **FW1:** Framework-owned paths are replaced on update: `internal/app/`, `internal/pkg/service-manager/`, `pkg/runner/`, `cmd/main.go`, `Makefile.servicepack`, `scripts/make/servicepack/`, `Dockerfile.servicepack*` and `servicepack.version`. Do not patch them for project behavior.
- **FW2:** Project changes go in the extension points: `internal/pkg/services/pr0xteus/`, `cmd/init.go` (startup hooks), `cmd/commands.go` (Cobra commands), `Makefile`, `scripts/make/` overrides, `Dockerfile`, `Dockerfile.dev`. `.servicepackupdateignore` keeps `cmd/init.go`, `cmd/commands.go`, `.golangci.yml`, the pipeline, `.agents`, `.dockerignore`, `.gitleaks.toml` and `FUNDING.yml` out of the sync; the update never touches `docs/`, `tests/`, `README.md` or `CHANGELOG.md` (`scripts/make/servicepack/do_update.sh`).
- **FW3:** `scripts/make/test_coverage.sh` overrides the framework script of the same name. It builds the cell image, runs the unmodified `scripts/make/servicepack/test_coverage.sh`, then runs `scripts/test-wrapper-upgrade.sh`. Keep project steps there, not in the framework copy.
- **FW4:** `internal/pkg/services/services.gen.go` is generated. Regenerate it with `make service-registration`; never edit it.
- **FW5:** `Run(ctx)` returns when its context is cancelled and `Stop(ctx)` does bounded cleanup. The controller's `Stop` closes the four listeners, shuts down the reaper and kills the hot cells; a new background loop joins that shutdown path.
- **FW6:** Typed configuration goes through `gonfiguration` struct tags in `config.go`. Do not add direct `os.Getenv` calls.
- **FW7:** The repository commits `vendor/`. Change modules with `make dep` and the `make pkg-*` targets, never by editing `go.mod`, `go.sum` or vendored sources by hand. The `pkg-*` targets run `scripts/check-go-age.sh`, which refuses third-party module versions younger than 7 days.
