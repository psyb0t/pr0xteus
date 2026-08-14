#!/bin/bash
set -euo pipefail

readonly log_file="${LOG_FILE:-/tmp/pr0xteus-merge-coverage.log}"
readonly coverage_pattern='^github.com/psyb0t/pr0xteus/(internal/pkg/services/pr0xteus|pkg/client)/'

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

temporary_profile=""

cleanup() {
	if [[ -n "$temporary_profile" ]]; then
		rm -f -- "$temporary_profile"
	fi
}

trap cleanup EXIT
trap 'log ERROR "command failed"' ERR
exec > >(tee -a "$log_file") 2>&1

if [[ "$#" -lt 3 ]]; then
	log ERROR "usage: merge_coverage.sh OUTPUT_PROFILE INPUT_PROFILE [INPUT_PROFILE...]"
	exit 2
fi

readonly output_profile="$1"
shift

for input_profile in "$@"; do
	if [[ ! -f "$input_profile" ]]; then
		log ERROR "coverage input does not exist path=$input_profile"
		exit 2
	fi
done

temporary_profile=$(mktemp "${output_profile}.tmp.XXXXXX")

awk -v pattern="$coverage_pattern" '
	$1 ~ pattern {
		key = $1 SUBSEP $2
		if (!(key in counts) || $3 > counts[key]) {
			counts[key] = $3
		}
		blocks[key] = $1 " " $2
	}
	END {
		for (key in blocks) {
			print blocks[key], counts[key]
		}
	}
' "$@" | sort -k1,1 -k2,2 >"$temporary_profile"

if [[ ! -s "$temporary_profile" ]]; then
	log ERROR "no pr0xteus production coverage blocks were found"
	exit 1
fi

printf 'mode: atomic\n' >"$output_profile"
cat "$temporary_profile" >>"$output_profile"
log INFO "merged pr0xteus production coverage profile path=$output_profile"
