---
title: "Development"
description: "Build, lint and test pr0xteus from source with Docker-backed Make targets, the Testcontainers suites, the coverage gate, the docs build and the CI pipeline."
nav_order: 11
has_children: true
---

# Development

The supported everyday workflow runs Go tooling inside the project's Docker development image. That removes host-version drift and means a clean machine needs Docker rather than a separately curated Go toolchain.

The Makefile has two execution shapes:

- ordinary build, lint, formatting, dependency, and vulnerability work runs in the development container with the repository mounted at `/work`;
- `make test-unit` uses the ordinary socketless container; full, integration, and coverage targets mount the Docker socket for Testcontainers.

The exact scripts and override lookup live in the [framework Make-script deep dive](https://github.com/psyb0t/pr0xteus/blob/main/scripts/make/servicepack/README.md). Contributor rules for agents and people are in [`AGENTS.md`](https://github.com/psyb0t/pr0xteus/blob/main/AGENTS.md) and [`.agents/rules/`](https://github.com/psyb0t/pr0xteus/tree/main/.agents/rules).

## Common targets

| Target | Runs where | Purpose |
| --- | --- | --- |
| `make dev-image` | Docker build | Build the development image used by normal tooling. |
| `make test` | Docker + socket | Race-enabled `go test ./...`. |
| `make test-unit` | Docker | Race-enabled `go test ./...` without Docker-socket access. |
| `make test-integration` | Docker + socket | Uncached, race-enabled test run with a ten-minute timeout. |
| `make test-coverage` | Docker + socket | Builds the cell image, runs unit and `integration`-tagged tests with coverage (floor `MIN_TEST_COVERAGE=90`), then the wrapper upgrade tests. |
| `make test-api` | Docker + socket | The production-image API suite in `tests/controlapi`. |
| `make sec` | Docker | govulncheck and semgrep, merged into `sec.sarif`. |
| `make build-cell` | Docker + socket | Build `psyb0t/pr0xteus:cell-dev`. |
| `make audit-compose` | Docker | Check `docker-compose.yml` against the hardened Compose floor. |
| `make docs` | Docker | Build the documentation site into `.testing/docs-site`. |
| `make lint` | Docker | `shfmt`, ShellCheck, `go fix` diff check, and golangci-lint. |
| `make lint-fix` | Docker | Apply supported formatting and lint fixes. Review its diff. |
| `make format` | Docker | Run gofumpt and shfmt. |
| `make generate` | Docker | Execute package-local `go:generate` directives. |
| `make build` | Docker | Build a static executable under `build/`. |
| `make docker-build` | Docker | Build the production image. |
| `make run-dev` | Docker | Build/run the development image with the race detector. |
| `make config-init` | Docker + socket | Generate ignored local operator config using the locally built controller image. |
| `make run` | Docker + socket | Build the local controller and cell images, then start the installer-equivalent dev stack. |
| `make restart` / `make stop` / `make status` | Docker + socket | Control only that local `pr0xteus-dev` stack. |

Use `make help` as the current target list. Do not replace these calls with a host `go test` or host linter in project automation: that gives two different toolchains two chances to disagree.

`make test` and `make test-integration` run every package without the `integration` build tag, so the suites under `tests/` are compiled out of them. `make test-coverage` and `make test-api` set the tag. None of them needs a local stack, so CI runs `make test-coverage`, which also measures the Go coverage and gates it at the floor. `make test-installed` is the exception: it drives the live installer stack, so it resets and starts that stack before its suite through the same wrapper as `make restart` and always calls `make stop` afterward, including on failure or interruption. `make test-api` and `make test-real` are standalone Testcontainers suites you call on their own.

`make run` is the operator path against local images. It runs the real installer in `PR0XTEUS_ENV=dev`, which builds the local controller and cell, creates ignored `.config/pr0xteus/`, and installs the same wrapper there. Make then runs `.config/pr0xteus/pr0xteus start` with that config directory. The only development differences are the `:dev`/`:cell-dev` image pin and Compose's `missing` pull policy, which preserves those local images. Tailscale remains a generated `.env` switch, just like an installed stack. This local config is separate from an operator's `~/.config/pr0xteus` stack.

## Tests, integration tests, and coverage

`make test` is the standard full suite. It enables Go's race detector and has Docker available for Testcontainers-backed tests. `make test-integration` uses the same package pattern but disables the Go test cache and applies a ten-minute timeout; use it when checking changes to real-infrastructure tests. Use `make test-unit` for the same race-enabled package walk without exposing the Docker socket; Testcontainers-backed cases cannot start infrastructure in that target.

`make test-coverage` also runs the race detector. It instruments every module package (`-coverpkg=<module>/...`) and runs `-tags=integration`, so a test under `tests/` credits coverage to the production package it drives, and **services are covered**: a service-heavy project does not need to override the script to gate its own code. A service that runs out-of-process in a real container is covered too: the script exports `SERVICEPACK_COVDATA_DIR`, an integration test mounts it into that container as `GOCOVERDIR`, and the native covdata is merged into the total. Only what is not hand-written code under test is excluded from the floor: `cmd/` mains, the `tests/` harness, generated `*.gen.go`, the service-manager mocks, and the framework's own `example-*` / `hello-world` demo services. Override the threshold deliberately:

```bash
make test-coverage MIN_TEST_COVERAGE=95
```

The target writes `coverage-percent.txt` for the badge workflow and removes the temporary coverage profiles. Testcontainers needs access to the Docker daemon; if Docker cannot create containers, fix Docker access rather than working around the integration tests.

## Build outputs and runtime identity

`make build` uses the pinned Go build image, installs the static-build requirements inside that temporary container, and writes `build/<module-tail>` back with your host UID/GID. It injects three values with linker flags:

- `main.appName`: the final segment of the module path; it sets the binary name and root Cobra command name;
- `main.buildCommit`: the checked-out `HEAD` commit when available.
- `main.buildVersion`: the exact Git tag at `HEAD`, or `dev` for an untagged source build. The controller uses it to pick its cell image (`cell-<version>`); CI image builds set it to `sha-<commit>`.

`cmd/main.go` puts those into the global log scope as `binary`, `commit`, and `version`. Build output is therefore traceable without hardcoding identity in service code. A source tree without a resolvable Git `HEAD` builds, but has no commit field to inject.

`make docker-build` is a separate production-image path. Do not assume a Dockerfile build and `make build` have the same customization hooks; inspect the relevant script before changing either.

## Dependencies and code generation

Use the Make targets so dependency metadata and `vendor/` stay synchronized:

```bash
make dep
make pkg-add PKG=example.com/module@v1.2.3
make pkg-update PKG=example.com/module
make pkg-upgrade
make pkg-remove PKG=example.com/module
```

The framework commits Go's `vendor/` tree. Do not edit vendored sources by hand. `make service` already regenerates service registration; use `make service-registration` after manual service changes. Treat `internal/pkg/services/services.gen.go` as generated output.

## Override, do not fork framework plumbing

`Makefile` includes `Makefile.servicepack`. Define the same target in your project Makefile to replace a framework target, or add your own targets next to it. Framework script lookup prefers `scripts/make/<script>.sh` over `scripts/make/servicepack/<script>.sh`.

```bash
cp scripts/make/servicepack/test.sh scripts/make/test.sh
# edit scripts/make/test.sh for project-specific behavior
```

Likewise, project `Dockerfile`, `Dockerfile.dev`, `cmd/init.go`, and `cmd/commands.go` are yours. The framework-owned versions are updated by `make servicepack-update`; see [framework updates](framework-updates.md).

## Before you hand off a change

For a normal code change, run the narrowest useful target first, then the relevant full check, for example `make test`, `make lint`, and `make test-coverage` when a framework behavior changes. The repository's pre-commit hook calls `make lint && make test-coverage`, so it will repeat those checks during the project's normal commit flow.

Read [Servicepack basics](getting-started.md) for ownership boundaries and [Servicepack architecture](architecture.md) for where a change should live.

## Production-image API tests and real-provider tests

`make test-api` uses Testcontainers to build and start the production controller image, a self-contained WireGuard peer container (built from [`tests/testinfra/wireguard/`](https://github.com/psyb0t/pr0xteus/tree/main/tests/testinfra/wireguard)), the cell image, and a sibling proxy client on an isolated Docker network. The API test drives every control-plane route over real HTTP and proves that SOCKS5 and HTTP-proxy traffic traverse the WireGuard tunnel to a private test HTTP server on the peer. It needs no provider account, real WireGuard bundle, host port, or persistent container, and Testcontainers tears down only the exact resources it created.

Coverage runs substitute the instrumented `Dockerfile.integration` for the production controller image and merge its native coverage into the overall 90% gate. Normal `test-api` runs use the production `Dockerfile`. See [coverage](#tests-integration-tests-and-coverage) for the package selection and exclusions.

`make test-real` is deliberately separate from `make test` and CI. It loads the ignored local provider bundle at `secrets/wg/provider-wireguard/` with the matching `secrets/wg/pools.yaml` and `config/egress-routing.yaml`, starts its own Testcontainers controller and consumer, requests a real egress proxy, then verifies that the consumer's public IPv4 address changes when traffic uses both returned proxy URLs. Set `PR0XTEUS_REAL_TEST_COUNTRY=US` before running `make test-real` to select the routing input. It creates a fresh test token and a unique controller scope; it neither reads the production token nor touches a running stack.

The public Go client is under [`pkg/client`](https://github.com/psyb0t/pr0xteus/tree/main/pkg/client) and documented on the [Go client](go-client.md) page; it is useful when another Go service should request and retry egress proxies without re-implementing the HTTP contract.

## Documentation site

`docs/` is the documentation site. GitHub Pages builds it from `main` with Jekyll and the just-the-docs theme. Every page needs front matter with a unique `title` and a `description` of 50 to 170 characters. Page-to-page links use the relative `.md` path; a link to a file outside `docs/` uses the full GitHub URL. `make docs` builds the site with the same image GitHub uses, `ghcr.io/actions/jekyll-build-pages`, into the ignored `.testing/docs-site`.

## Continuous integration

[`.github/workflows/pipeline.yml`](https://github.com/psyb0t/pr0xteus/blob/main/.github/workflows/pipeline.yml) runs on every push:

- Every branch: `make lint`, `make test-coverage`, `make sec` (SARIF uploaded to code scanning) and `make generate`, which fails on any drift in committed generated files. CI needs no secrets or WireGuard material.
- `main`, after the checks pass: builds the cell and controller images for `linux/amd64` and `linux/arm64` and pushes `:latest`, `:cell-latest`, `:sha-<commit>` and `:cell-sha-<commit>` to Docker Hub, then refreshes the version, license and coverage badges.
- A `v*` tag: waits for that commit's `main` run to succeed, points `:vX.Y.Z` and `:cell-vX.Y.Z` at the same image digests without rebuilding, creates the GitHub release, refreshes the badges and publishes the `pr0xteus` skill to ClawHub.

The mirror, issue relay and collaborator workflows next to it are independent of this pipeline.
