#!/bin/bash
set -euo pipefail

readonly LOG_FILE="${TMPDIR:-/tmp}/pr0xteus-test-local-lifecycle.log"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
readonly SCRIPT_DIR
REPOSITORY_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
readonly REPOSITORY_DIR

cleanup_enabled=0

log() {
	local level="$1"
	shift
	printf '{"time":"%s","level":"%s","file":"test-local-lifecycle.sh","line":%d,"func":"%s","msg":"%s"}\n' \
		"$(date -u '+%Y-%m-%dT%H:%M:%SZ')" "$level" "${BASH_LINENO[0]}" \
		"${FUNCNAME[1]:-main}" "$*" >&2
}

usage() {
	printf 'Usage: test-local-lifecycle.sh -- <test-command> [args...]\n'
}

cleanup() {
	local status=$?

	trap - EXIT
	if ((cleanup_enabled)); then
		log INFO "stopping the local test stack"
		if ! make --directory "$REPOSITORY_DIR" stop; then
			log ERROR "could not stop the local test stack"
			if ((status == 0)); then
				status=1
			fi
		fi
	fi

	exit "$status"
}

on_error() {
	local status=$?
	log ERROR "test lifecycle command failed exit=$status"
	return "$status"
}

on_interrupt() {
	log WARN "test lifecycle interrupted"
	exit 130
}

on_terminate() {
	log WARN "test lifecycle terminated"
	exit 143
}

main() {
	[[ "${1:-}" == "--" ]] || {
		usage >&2
		return 2
	}
	shift
	[[ "$#" -gt 0 ]] || {
		usage >&2
		return 2
	}

	cleanup_enabled=1
	log INFO "resetting and starting the local test stack"
	make --directory "$REPOSITORY_DIR" restart
	"$@"
}

trap on_error ERR
trap cleanup EXIT
trap on_interrupt INT
trap on_terminate TERM
exec > >(
	trap '' INT TERM
	tee -a "$LOG_FILE"
) 2>&1

main "$@"
