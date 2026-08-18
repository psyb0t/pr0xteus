#!/bin/bash
set -euo pipefail

readonly log_file="${LOG_FILE:-/tmp/pr0xteus-audit-compose.log}"
readonly compose_file="${1:-docker-compose.yml}"
# shellcheck disable=SC2016  # literal string matched against compose lines; ${...} must NOT expand
readonly controller_image_reference='image: ${PR0XTEUS_CONTROLLER_IMAGE:'

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

require_pattern() {
	local pattern="$1"
	local message="$2"

	if ! rg -q --pcre2 "$pattern" "$compose_file"; then
		log ERROR "$message"
		exit 1
	fi
}

service_has_network() {
	local service="$1"
	local network="$2"

	awk -v service="$service" -v network="$network" '
		$0 == "  " service ":" { in_service = 1; next }
		in_service && /^  [[:alnum:]_-]+:$/ { exit }
		in_service && $0 ~ "^[[:space:]]+- " network "$" { found = 1 }
		END { exit !found }
	' "$compose_file"
}

[[ -f "$compose_file" ]] || {
	log ERROR "compose file does not exist path=$compose_file"
	exit 2
}

if rg -n --pcre2 \
	'privileged:\s*true|pid:\s*host|ipc:\s*host|network_mode:\s*host|userns_mode:\s*host|cgroup:\s*host|SYS_ADMIN' \
	"$compose_file"; then
	log ERROR "banned Compose privilege setting found"
	exit 1
fi

if rg -n --pcre2 '^\s+-\s+"(?!127\.0\.0\.1:)[0-9]+:[0-9]+"' "$compose_file"; then
	log ERROR "non-loopback published port found"
	exit 1
fi

raw_socket_mounts=$(rg -c --no-filename '/var/run/docker\.sock' "$compose_file" || true)
if [[ "$raw_socket_mounts" != "2" ]]; then
	log ERROR "expected exactly one raw Docker socket bind mount"
	exit 1
fi

if ! awk '
  /^  docker-socket-proxy:$/ { in_proxy = 1; next }
  /^  [[:alnum:]_-]+:$/ { in_proxy = 0 }
  in_proxy && /source: \/var\/run\/docker\.sock/ { found = 1 }
  END { exit !found }
' "$compose_file"; then
	log ERROR "raw Docker socket is not limited to docker-socket-proxy"
	exit 1
fi

if service_has_network pr0xteus egress; then
	log ERROR "controller must NOT join egress; it reaches cells over the internal cell-control network"
	exit 1
fi

if ! service_has_network pr0xteus cell-control; then
	log ERROR "controller must join cell-control to probe private cell listeners"
	exit 1
fi

if service_has_network docker-socket-proxy egress; then
	log ERROR "docker socket proxy must stay on the internal control network"
	exit 1
fi

if ! service_has_network egress-network-anchor egress; then
	log ERROR "egress anchor must create the named network required by dynamic cells"
	exit 1
fi

if service_has_network egress-network-anchor control ||
	service_has_network egress-network-anchor cell-control ||
	service_has_network egress-network-anchor tailnet; then
	log ERROR "egress anchor must remain isolated from controller and Tailnet networks"
	exit 1
fi

if ! service_has_network tailscale control || ! service_has_network tailscale tailnet; then
	log ERROR "Tailscale must bridge only the control and tailnet networks"
	exit 1
fi

if service_has_network pr0xteus tailnet || service_has_network docker-socket-proxy tailnet; then
	log ERROR "only the Tailscale sidecar may join the tailnet network"
	exit 1
fi

while IFS= read -r image_line; do
	if [[ "$image_line" == *"$controller_image_reference"* ]]; then
		continue
	fi

	if [[ "$image_line" != *'@sha256:'* ]]; then
		log ERROR "external image is not digest-pinned detail=$image_line"
		exit 1
	fi
done < <(rg -n '^\s+image:\s+' "$compose_file")

require_pattern 'cap_drop:\s*\[ALL\]' 'every service needs cap_drop: [ALL]'
require_pattern 'no-new-privileges:true' 'every service needs no-new-privileges'
require_pattern 'read_only:\s*true' 'every service needs read_only: true'
require_pattern 'init:\s*true' 'every service needs init: true'
require_pattern 'mem_limit:' 'every service needs a memory limit'
require_pattern 'pids_limit:' 'every service needs a PID limit'
require_pattern 'max-size:\s*"10m"' 'every service needs capped Docker JSON logs'
require_pattern 'internal:\s*true' 'control networks must remain internal'
require_pattern 'name:\s*pr0xteus-cell-control' 'cell-control network must be declared'
require_pattern 'PR0XTEUS_API_TOKEN:' 'controller token must come from ignored .env'
require_pattern 'PR0XTEUS_CONFIG_DIR:' 'controller config root must be explicit'
require_pattern 'PR0XTEUS_HTTP_HOST_PORT:-127\.0\.0\.1:8000' 'HTTP port must default to loopback'
require_pattern 'PR0XTEUS_METRICS_HOST_PORT:-127\.0\.0\.1:9091' 'metrics port must default to loopback'
require_pattern 'PR0XTEUS_SOCKS_HOST_PORT:-127\.0\.0\.1:1080' 'SOCKS port must default to loopback'
require_pattern 'profiles:\s*\["tailscale"\]' 'Tailscale must stay opt-in'
require_pattern 'TS_STATE_DIR:\s*/var/lib/tailscale' 'Tailscale state must persist'
require_pattern 'TS_USERSPACE:\s*"false"' 'Tailscale must use its own kernel tunnel'
require_pattern 'target:\s*/dev/net/tun' 'Tailscale needs the explicit TUN device'

log INFO "Compose static hardening audit passed path=$compose_file"
