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
DEV_CONFIG_DIR ?= $(CURDIR)/.config/pr0xteus
DEV_ENV := PR0XTEUS_ENV=dev PR0XTEUS_SOURCE_DIR=$(CURDIR) PR0XTEUS_HOME=$(DEV_CONFIG_DIR)

include Makefile.servicepack

DEV_RUN_DIND_EXTRA_ARGS := \
	-e PR0XTEUS_REAL_TEST_COUNTRY \
	-e PR0XTEUS_REAL_TEST_ENABLED

# Only test-installed needs a running installer stack: it drives the live
# http://pr0xteus:8000, so it wraps itself in this lifecycle (reset + start,
# then stop after). Every other test target is self-contained through
# Testcontainers and must NOT start a real stack, so CI, which has no wireguard
# secrets to boot one, can run them. A test that needs `make restart` is a
# local-only test, not a CI test.
LOCAL_TEST_LIFECYCLE := bash scripts/test-local-lifecycle.sh --

.PHONY: test-api test-real test-installed build-cell config-init run restart stop status audit-compose

# test-api builds pr0xteus from its production Dockerfile in Testcontainers,
# stands up a self-contained WireGuard peer container (no external provider),
# and drives every control-plane route over real HTTP. This is the API contract
# test; make test-real is the only one that uses a real external provider.
test-api: dev-image build-cell ## Build pr0xteus from its Dockerfile in Testcontainers and hit every HTTP route
	@$(DEV_RUN_DIND) bash -ceu 'mkdir -p $(COVERAGE_DIRECTORY); go test -tags=integration -race -count=1 -timeout=600s ./tests/controlapi 2>&1 | tee $(API_TEST_LOG)'

test-real: dev-image build-cell ## Opt-in real external-provider egress smoke test
	@PR0XTEUS_REAL_TEST_ENABLED=true $(DEV_RUN_DIND) bash -ceu 'mkdir -p $(COVERAGE_DIRECTORY); go test -tags=integration,real -race -count=1 -timeout=600s ./tests/real 2>&1 | tee $(REAL_TEST_LOG)'

test-installed: dev-image ## Prove the installer-created stack API and real proxy egress
	@$(LOCAL_TEST_LIFECYCLE) docker run --rm --init --network pr0xteus-control \
		--user $(UID):$(GID) \
		-e PR0XTEUS_TEST_BASE_URL=http://pr0xteus:8000 \
		-e PR0XTEUS_TEST_METRICS_URL=http://pr0xteus:9091 \
		-e PR0XTEUS_TEST_PROXY_HOST=pr0xteus \
		-e PR0XTEUS_TEST_DIRECT_IP="$$(docker run --rm $(DEV_IMAGE) curl --ipv4 --fail --silent --show-error https://api.ipify.org)" \
		-e PR0XTEUS_TEST_PUBLIC_IP_ADDRESS="$$(docker run --rm $(DEV_IMAGE) getent ahostsv4 api.ipify.org | awk 'NR == 1 {print $$1}')" \
		-v "$(CURDIR):/work:ro" \
		-v "$(DEV_CONFIG_DIR):/config:ro" \
		-w /work \
		$(DEV_IMAGE) bash scripts/test-installed.sh

build-cell: dev-image ## Build the WireGuard plus SOCKS5 cell image through the development container
	@PR0XTEUS_CELL_TAG=$(CELL_IMAGE_NAME):$(CELL_TAG_PREFIX)$(TAG) $(DEV_RUN_DIND) bash scripts/build_cell.sh

config-init: ## Create ignored local config and wrapper through the real installer
	@$(DEV_ENV) bash install.sh --user

run: config-init ## Build local images and start through the shared production wrapper
	@$(DEV_ENV) bash $(DEV_CONFIG_DIR)/pr0xteus start

restart: config-init ## Rebuild local images and restart through the shared production wrapper
	@$(DEV_ENV) bash $(DEV_CONFIG_DIR)/pr0xteus restart

stop: ## Stop only the ignored local development stack through the generated wrapper
	@$(DEV_ENV) bash $(DEV_CONFIG_DIR)/pr0xteus stop

status: ## Show the ignored local development stack state through the generated wrapper
	@$(DEV_ENV) bash $(DEV_CONFIG_DIR)/pr0xteus status

audit-compose: dev-image ## Reject unsafe Compose settings in the development container
	@$(DEV_RUN) bash scripts/audit-compose.sh docker-compose.yml
