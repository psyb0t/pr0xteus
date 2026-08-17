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

DEV_RUN_DIND_EXTRA_ARGS := \
	-e PR0XTEUS_REAL_TEST_COUNTRY \
	-e PR0XTEUS_REAL_TEST_ENABLED

.PHONY: test-api test-real build-cell run config-init config-check audit-compose

# test-api builds pr0xteus from its production Dockerfile in Testcontainers,
# stands up a self-contained WireGuard peer container (no external provider),
# and drives every control-plane route over real HTTP. This is the API contract
# test; make test-real is the only one that uses a real Surfshark egress.
test-api: dev-image build-cell ## Build pr0xteus from its Dockerfile in Testcontainers and hit every HTTP route
	@$(DEV_RUN_DIND) bash -ceu 'mkdir -p $(COVERAGE_DIRECTORY); go test -tags=integration -race -count=1 -timeout=600s ./tests/controlapi 2>&1 | tee $(API_TEST_LOG)'

test-real: dev-image build-cell ## Opt-in real Surfshark egress smoke test
	@PR0XTEUS_REAL_TEST_ENABLED=true $(DEV_RUN_DIND) bash -ceu 'mkdir -p $(COVERAGE_DIRECTORY); go test -tags=integration,real -race -count=1 -timeout=600s ./tests/real 2>&1 | tee $(REAL_TEST_LOG)'

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
