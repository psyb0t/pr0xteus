#!/bin/bash
set -euo pipefail

trap 'log ERROR "command failed"' ERR

readonly minimum_age_days=7
readonly milliseconds_suffix=".000Z"
LOG_FILE="${LOG_FILE:-/tmp/age-check-go-module.log}"
exec > >(tee -a "$LOG_FILE") 2>&1

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
	timestamp=$(date -u "+%Y-%m-%dT%H:%M:%S${milliseconds_suffix}")
	escaped_message=$(json_escape "$message")
	printf '{"time":"%s","level":"%s","file":"%s","line":%s,"func":"%s","msg":"%s"}\n' \
		"$timestamp" "$level" "${BASH_SOURCE[1]##*/}" "${BASH_LINENO[0]}" \
		"${FUNCNAME[1]:-main}" "$escaped_message" >&2
}

usage() {
	printf 'usage: %s module@version\n' "${0##*/}" >&2
}

[[ "$#" -eq 1 ]] || {
	usage
	exit 2
}

readonly module_version="$1"
readonly module="${module_version%@*}"
readonly version="${module_version##*@}"

[[ "$module" != "$module_version" && -n "$version" ]] || {
	log ERROR "expected module@version"
	exit 2
}

if [[ -n "${DEBUG:-}" ]]; then
	log DEBUG "checking publication age"
fi

metadata=$(GONOSUMDB='' GOPROXY=https://proxy.golang.org GOFLAGS=-mod=mod \
	go list -m -json "${module}@${version}")
published_at=$(printf '%s' "$metadata" | awk -F'"' '/"Time":/ {print $4; exit}')
[[ -n "$published_at" ]] || {
	log ERROR "could not find module publication time"
	exit 1
}

published_at_without_zone="${published_at%Z}"
normalized_published_at="${published_at_without_zone/T/ }"
published_epoch=$(date -u -d "$normalized_published_at" '+%s')
now_epoch=$(date -u '+%s')
age_days=$(((now_epoch - published_epoch) / 86400))

if ((age_days < minimum_age_days)); then
	log ERROR "module is too new for the supply-chain age gate"
	exit 1
fi

if ((age_days < minimum_age_days * 2)); then
	log WARN "module only just cleared the supply-chain age gate"
fi

log INFO "module age gate passed"
