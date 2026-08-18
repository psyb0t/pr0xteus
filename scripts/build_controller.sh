#!/bin/bash
set -euo pipefail

readonly log_file="${LOG_FILE:-/tmp/pr0xteus-build-controller.log}"
readonly controller_image="${PR0XTEUS_DEV_CONTROLLER_IMAGE:-psyb0t/pr0xteus:dev}"

log() {
	printf '[%s] %s\n' "$1" "$2" >&2
}

trap 'log ERROR "controller image build failed at line ${LINENO}"' ERR

[[ "$controller_image" == *:* ]] || {
	log ERROR "PR0XTEUS_DEV_CONTROLLER_IMAGE must include a tag"
	exit 1
}

build_commit="$(git rev-parse --verify HEAD 2>/dev/null || printf 'dev')"
log INFO "building local controller image ${controller_image}"
docker build \
	--build-arg "BUILD_COMMIT=${build_commit}" \
	--build-arg BUILD_VERSION=dev \
	--file Dockerfile \
	--tag "$controller_image" \
	. >"$log_file" 2>&1
log INFO "local controller image built"
