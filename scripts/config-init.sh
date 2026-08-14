#!/bin/bash
set -euo pipefail

readonly log_file="${LOG_FILE:-/tmp/pr0xteus-config-init.log}"

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

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
readonly script_dir
project_dir="$(cd "${script_dir}/.." && pwd)"
readonly project_dir
readonly generic_bundle_dir="${project_dir}/secrets/wireguard"
readonly legacy_bundle_dir="${project_dir}/secrets/wg/surfshark-wireguard"
readonly generic_pools_file="${project_dir}/secrets/pools.yaml"
readonly legacy_pools_file="${project_dir}/secrets/wg/pools.yaml"
readonly routing_file="${project_dir}/config/egress-routing.yaml"
readonly example_pools_file="${project_dir}/examples/pools.yaml"
readonly example_routing_file="${project_dir}/examples/egress-routing.yaml"
readonly token_file="${project_dir}/secrets/pr0xteus_api_token"
readonly env_file="${project_dir}/.env"

if [[ -n "${DEBUG:-}" ]]; then
	log DEBUG "checking local configuration shape"
fi

if [[ -e "$generic_bundle_dir" ]]; then
	bundle_dir="$generic_bundle_dir"
else
	bundle_dir="$legacy_bundle_dir"
fi

if [[ -e "$generic_pools_file" ]]; then
	pools_file="$generic_pools_file"
else
	pools_file="$legacy_pools_file"
fi

if [[ ! -e "$bundle_dir" && ! -e "$pools_file" && ! -e "$routing_file" ]]; then
	mkdir -p "$generic_bundle_dir" "${generic_pools_file%/*}" "${routing_file%/*}"
	cp "$example_pools_file" "$generic_pools_file"
	cp "$example_routing_file" "$routing_file"
	bundle_dir="$generic_bundle_dir"
	pools_file="$generic_pools_file"
	log WARN "created ignored example configuration; add a WireGuard .conf and edit secrets/pools.yaml"
fi

for required_path in "$bundle_dir" "$pools_file" "$routing_file"; do
	if [[ ! -e "$required_path" ]]; then
		log ERROR "required ignored configuration is missing path=$required_path"
		exit 1
	fi
done

if [[ -e "$token_file" ]]; then
	log WARN "preserving existing API token file"
else
	umask 077
	mkdir -p "${token_file%/*}"
	od -An -N32 -tx1 /dev/urandom | tr -d ' \n' >"$token_file"
	chmod 0600 "$token_file"
	log INFO "created ignored API token file"
fi

if [[ -e "$env_file" ]]; then
	if grep -Fxq 'PR0XTEUS_CELL_IMAGE=psyb0t/pr0xteus-cell:dev' "$env_file"; then
		log WARN "existing .env uses the retired local cell tag; change it to psyb0t/pr0xteus:cell-dev"
	fi
	log WARN "preserving existing .env file"
else
	umask 077
	printf '%s\n' \
		'PR0XTEUS_CELL_IMAGE=psyb0t/pr0xteus:cell-dev' \
		'PR0XTEUS_ALLOW_UNPINNED_CELL_IMAGE=true' \
		"PR0XTEUS_BUNDLE_DIR=$bundle_dir" \
		"PR0XTEUS_POOLS_FILE=$pools_file" \
		"PR0XTEUS_ROUTING_FILE=$routing_file" \
		"PR0XTEUS_API_TOKEN_FILE=$token_file" \
		'PR0XTEUS_HTTP_PORT=8000' \
		'PR0XTEUS_METRICS_PORT=9091' \
		'LOG_LEVEL=info' \
		'LOG_FORMAT=json' \
		'LOG_ADD_SOURCE=true' >"$env_file"
	log INFO "created ignored local .env for the copied configuration"
fi

if compgen -G "${bundle_dir}/*.conf" >/dev/null; then
	log INFO "local configuration is ready; run make config-check next"
else
	log WARN "configuration skeleton is not runnable until a real WireGuard .conf is added"
fi
