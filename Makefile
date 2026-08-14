SHELL := /bin/bash

IMAGE_NAME := psyb0t/pr0xteus
CELL_IMAGE_NAME := $(IMAGE_NAME)
CELL_TAG_PREFIX := cell-
TAG ?= dev
DEV_IMAGE := pr0xteus-dev
MIN_TEST_COVERAGE := 90
COVERAGE_DIRECTORY := .cover
COVERAGE_PROFILE := $(COVERAGE_DIRECTORY)/coverage.out
COVERAGE_RAW_PROFILE := $(COVERAGE_DIRECTORY)/coverage.raw.out
CONTROLLER_COVERAGE_DIRECTORY := $(COVERAGE_DIRECTORY)/controller
CONTROLLER_COVERAGE_PROFILE := $(COVERAGE_DIRECTORY)/controller-coverage.out
INTEGRATION_TEST_LOG := $(COVERAGE_DIRECTORY)/test-integration.log
COVERAGE_TEST_LOG := $(COVERAGE_DIRECTORY)/test-coverage.log
REAL_TEST_LOG := $(COVERAGE_DIRECTORY)/test-real.log

include Makefile.servicepack

DEV_RUN_DIND_INTEGRATION := docker run --rm --init \
	--user $(UID):$(GID) \
	--group-add $(DOCKER_GID) \
	-e HOME=/tmp \
	-e GOCACHE=/tmp/go-cache \
	-e GOMODCACHE=/tmp/go-mod-cache \
	-e DOCKER_HOST=unix://$(DOCKER_SOCK) \
	-e PR0XTEUS_REAL_TEST_COUNTRY \
	-e PR0XTEUS_REAL_TEST_ENABLED \
	-e TESTCONTAINERS_DOCKER_SOCKET_OVERRIDE=$(DOCKER_SOCK) \
	-e TESTCONTAINERS_RYUK_DISABLED=true \
	-v "$(CURDIR):$(CURDIR)" \
	-v "$(DOCKER_SOCK):$(DOCKER_SOCK)" \
	-w "$(CURDIR)" \
	$(DEV_IMAGE)

.PHONY: test-real coverage-report docker-build build-cell run config-init config-check audit-compose

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

test-real: dev-image build-cell ## Opt-in real Surfshark egress smoke test
	@PR0XTEUS_REAL_TEST_ENABLED=true $(DEV_RUN_DIND_INTEGRATION) bash -ceu 'mkdir -p $(COVERAGE_DIRECTORY); go test -tags=integration,real -race -count=1 -timeout=600s ./tests/real 2>&1 | tee $(REAL_TEST_LOG)'

test-coverage: dev-image build-cell ## Require 90% pr0xteus coverage using unit and Testcontainers paths
	@$(DEV_RUN_DIND_INTEGRATION) bash -ceu -o pipefail 'run_dir="$(CONTROLLER_COVERAGE_DIRECTORY)/run-$$(date +%s)-$$$$"; mkdir -p "$$run_dir" $(COVERAGE_DIRECTORY); export PR0XTEUS_TEST_COVERAGE_OUTPUT_DIR="$(CURDIR)/$$run_dir"; go test -tags=integration -race -count=1 -timeout=600s -coverpkg=./internal/pkg/services/pr0xteus/...,./pkg/client/... -coverprofile=$(COVERAGE_RAW_PROFILE) ./... 2>&1 | tee $(COVERAGE_TEST_LOG); go tool covdata textfmt -i="$$run_dir" -o $(CONTROLLER_COVERAGE_PROFILE); bash scripts/merge_coverage.sh $(COVERAGE_PROFILE) $(COVERAGE_RAW_PROFILE) $(CONTROLLER_COVERAGE_PROFILE); raw=$$(go tool cover -func=$(COVERAGE_PROFILE) | awk "/^total:/ {print \$$3}"); pct=$${raw%\%}; test -n "$$pct"; printf "coverage: %s%% (required: %s%%)\\n" "$$pct" "$(MIN_TEST_COVERAGE)"; printf "%s%%\\n" "$$pct" > coverage-percent.txt; if ! awk "BEGIN { exit !($$pct >= $(MIN_TEST_COVERAGE)) }"; then printf "coverage gate failed: %s%% is below %s%%\\n" "$$pct" "$(MIN_TEST_COVERAGE)" >&2; exit 1; fi'

coverage-report: dev-image ## Report coverage from the latest ignored artifact
	@$(DEV_RUN) go tool cover -func=$(COVERAGE_PROFILE)

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
