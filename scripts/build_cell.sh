#!/bin/bash
# Build the pr0xteus cell image (ephemeral SOCKS5 + WireGuard worker).
# Tagged psyb0t/pr0xteus:cell-dev for local development. The orchestrator
# references this tag through PR0XTEUS_CELL_IMAGE in ignored local .env.
#
# Run from anywhere — resolves the cell/ Dockerfile relative to this
# script's location.
set -euo pipefail
readonly log_file="${LOG_FILE:-/tmp/pr0xteus-build-cell.log}"

json_escape() {
	local value="$1"
	value="${value//\\/\\\\}"
	value="${value//\"/\\\"}"
	value="${value//$'\n'/\\n}"
	value="${value//$'\r'/\\r}"
	value="${value//$'\t'/\\t}"
	printf '%s' "$value"
}

log() {
	local level="$1"
	shift
	local message="$*"
	local timestamp escaped_message
	timestamp=$(date -u '+%Y-%m-%dT%H:%M:%S.000Z')
	escaped_message=$(json_escape "$message")
	printf '{"time":"%s","level":"%s","file":"%s","line":%s,"func":"%s","msg":"%s"}\n' \
		"$timestamp" "$level" "${BASH_SOURCE[1]##*/}" "${BASH_LINENO[0]}" \
		"${FUNCNAME[1]:-main}" "$escaped_message" >&2
}

trap 'log ERROR "command failed"' ERR
exec > >(tee -a "$log_file") 2>&1

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CELL_DIR="${SCRIPT_DIR}/../cell"
CELL_TAG="${PR0XTEUS_CELL_TAG:-psyb0t/pr0xteus:cell-dev}"

[[ -f "${CELL_DIR}/Dockerfile" ]] || {
	log ERROR "cell Dockerfile not found"
	exit 1
}

if [[ -n "${DEBUG:-}" ]]; then
	log DEBUG "building local cell image"
fi
if [[ "$CELL_TAG" != *@sha256:* ]]; then
	log WARN "building a mutable local development tag"
fi
log INFO "building local cell image"
docker build -t "${CELL_TAG}" -f "${CELL_DIR}/Dockerfile" "${CELL_DIR}"
log INFO "local cell image built"
