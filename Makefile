SHELL := /bin/bash

IMAGE_NAME := psyb0t/pr0xteus
CELL_IMAGE_NAME := $(IMAGE_NAME)
CELL_TAG_PREFIX := cell-
TAG ?= dev
DEV_IMAGE := pr0xteus-dev
MIN_TEST_COVERAGE := 90
COVERAGE_DIRECTORY := .cover
INTEGRATION_TEST_LOG := $(COVERAGE_DIRECTORY)/test-integration.log
API_TEST_LOG := $(COVERAGE_DIRECTORY)/test-api.log
REAL_TEST_LOG := $(COVERAGE_DIRECTORY)/test-real.log

include Makefile.servicepack

DEV_RUN_DIND_INTEGRATION := docker run --rm --init \
	--user $(UID):$(GID) \
	--group-add $(DOCKER_GID) \
	-e HOME=/tmp \
	-e GOCACHE=/tmp/go-cache \
	-e GOMODCACHE=/tmp/go-mod-cache \
	-e MIN_TEST_COVERAGE \
	-e DOCKER_HOST=unix://$(DOCKER_SOCK) \
	-e PR0XTEUS_REAL_TEST_COUNTRY \
	-e PR0XTEUS_REAL_TEST_ENABLED \
	-e TESTCONTAINERS_DOCKER_SOCKET_OVERRIDE=$(DOCKER_SOCK) \
	-e TESTCONTAINERS_RYUK_DISABLED=true \
	-v "$(CURDIR):$(CURDIR)" \
	-v "$(DOCKER_SOCK):$(DOCKER_SOCK)" \
	-w "$(CURDIR)" \
	$(DEV_IMAGE)

.PHONY: test-api test-real docker-build build-cell run config-init config-check audit-compose

format: dev-image ## Format Go and shell source in the development container
	@$(DEV_RUN) bash -ceu 'go tool gofumpt -w .; shfmt -w install.sh $$(find cell scripts -type f -name "*.sh")'

lint: dev-image ## Run Go, shell, and Compose static checks in the development container
	@$(DEV_RUN) bash -ceu 'out=$$(go fix -diff ./... 2>&1) || true; test -z "$$out" || { printf "%s\\n" "$$out" >&2; exit 1; }; go tool golangci-lint run --timeout=30m0s ./...; shell_files="install.sh $$(find cell scripts -type f -name "*.sh" -print)"; shellcheck -x -P scripts/make/servicepack $$shell_files; shfmt -d $$shell_files; bash scripts/audit-compose.sh docker-compose.yml'

lint-fix: dev-image ## Apply safe Go and shell fixes in the development container
	@$(DEV_RUN) bash -ceu 'go fix ./...; go tool golangci-lint run --fix --timeout=30m0s ./...; shell_files="install.sh $$(find cell scripts -type f -name "*.sh" -print)"; shfmt -w $$shell_files; shellcheck -x -P scripts/make/servicepack $$shell_files; bash scripts/audit-compose.sh docker-compose.yml'

test: test-unit test-integration ## Run unit and Testcontainers integration suites
	@:

test-unit: dev-image ## Run the race-enabled unit suite in the development container
	@$(DEV_RUN) go test -race ./...

test-integration: dev-image build-cell ## Run the WireGuard plus SOCKS5 Testcontainers suite
	@$(DEV_RUN_DIND_INTEGRATION) bash -ceu 'mkdir -p $(COVERAGE_DIRECTORY); go test -tags=integration -race -count=1 -timeout=600s ./tests/... 2>&1 | tee $(INTEGRATION_TEST_LOG)'

# test-api builds pr0xteus from its production Dockerfile in Testcontainers,
# stands up a self-contained WireGuard peer container (no external provider),
# and drives every control-plane route over real HTTP. This is the API contract
# test; make test-real is the only one that uses a real Surfshark egress.
test-api: dev-image build-cell ## Build pr0xteus from its Dockerfile in Testcontainers and hit every HTTP route
	@$(DEV_RUN_DIND_INTEGRATION) bash -ceu 'mkdir -p $(COVERAGE_DIRECTORY); go test -tags=integration -race -count=1 -timeout=600s ./tests/controlapi 2>&1 | tee $(API_TEST_LOG)'

test-real: dev-image build-cell ## Opt-in real Surfshark egress smoke test
	@PR0XTEUS_REAL_TEST_ENABLED=true $(DEV_RUN_DIND_INTEGRATION) bash -ceu 'mkdir -p $(COVERAGE_DIRECTORY); go test -tags=integration,real -race -count=1 -timeout=600s ./tests/real 2>&1 | tee $(REAL_TEST_LOG)'

# test-coverage runs the servicepack coverage engine (covers every package,
# merges the controller's out-of-process covdata) inside pr0xteus's DIND +
# Testcontainers environment. The cell image must exist first for the live
# integration path.
test-coverage: dev-image build-cell ## Require 90% coverage across all pr0xteus packages
	@MIN_TEST_COVERAGE=$(MIN_TEST_COVERAGE) $(DEV_RUN_DIND_INTEGRATION) bash scripts/make/servicepack/test_coverage.sh

docker-build: ## Build the hardened production image with build identity
	@build_commit="$$(git rev-parse --verify HEAD 2>/dev/null || true)"; docker build --pull --build-arg "APP_NAME=pr0xteus" --build-arg "BUILD_COMMIT=$$build_commit" --build-arg "PR0XTEUS_BUILT_CELL_IMAGE=$(CELL_IMAGE_NAME):$(CELL_TAG_PREFIX)$(TAG)" -t $(IMAGE_NAME):$(TAG) .

build-cell: dev-image ## Build the WireGuard plus SOCKS5 cell image through the development container
	@PR0XTEUS_CELL_TAG=$(CELL_IMAGE_NAME):$(CELL_TAG_PREFIX)$(TAG) $(DEV_RUN_DIND) bash scripts/build_cell.sh

run: config-check docker-build build-cell ## Rebuild and start the private development stack
	@$(DEV_RUN_DIND) docker compose -f docker-compose.yml -f docker-compose.dev.yml up -d --build

config-init: docker-build ## Create ignored development configuration without overwriting it
	@docker run --rm --user $(UID):$(GID) \
		-v "$(CURDIR):/config" \
		$(IMAGE_NAME):$(TAG) config init \
		--config-dir /config \
		--host-config-dir "$(CURDIR)" \
		--development

config-check: docker-build dev-image ## Validate local config and production Compose interpolation
	@docker run --rm --user $(UID):$(GID) \
		-v "$(CURDIR):/config:ro" \
		$(IMAGE_NAME):$(TAG) config check --config-dir /config
	@$(DEV_RUN) docker compose -f docker-compose.yml config --quiet

audit-compose: dev-image ## Reject unsafe Compose settings in the development container
	@$(DEV_RUN) bash scripts/audit-compose.sh docker-compose.yml
