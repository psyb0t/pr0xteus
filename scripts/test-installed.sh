#!/bin/bash
set -euo pipefail

readonly LOG_FILE="${TMPDIR:-/tmp}/pr0xteus-test-installed.log"
readonly CONFIG_FILE="${PR0XTEUS_TEST_CONFIG_FILE:-/config/.env}"
readonly BASE_URL="${PR0XTEUS_TEST_BASE_URL:-http://127.0.0.1:8000}"
readonly METRICS_URL="${PR0XTEUS_TEST_METRICS_URL:-http://127.0.0.1:9091}"
readonly TEST_COUNTRY="${PR0XTEUS_REAL_TEST_COUNTRY:-US}"
readonly PROXY_HOST="${PR0XTEUS_TEST_PROXY_HOST:-}"
readonly DIRECT_IP="${PR0XTEUS_TEST_DIRECT_IP:-}"
readonly PUBLIC_IP_ADDRESS="${PR0XTEUS_TEST_PUBLIC_IP_ADDRESS:-}"
readonly READY_RETRIES=30

trap 'printf "{\"level\":\"ERROR\",\"file\":\"test-installed.sh\",\"line\":%d,\"msg\":\"command failed exit=%d\"}\n" "$LINENO" "$?" >&2' ERR
exec > >(tee -a "$LOG_FILE") 2>&1

log() {
	local level="$1"
	shift
	printf '{"time":"%s","level":"%s","file":"test-installed.sh","line":%d,"func":"%s","msg":"%s"}\n' \
		"$(date -u '+%Y-%m-%dT%H:%M:%SZ')" "$level" "${BASH_LINENO[0]}" "${FUNCNAME[1]:-main}" "$*" >&2
}

fail() {
	log ERROR "$*"
	exit 1
}

usage() {
	printf 'Usage: make test-installed [PR0XTEUS_TEST_BASE_URL=https://tailnet-name]\n'
}

env_value() {
	local key="$1"
	awk -F= -v key="$key" '$1 == key { print substr($0, index($0, "=") + 1); exit }' "$CONFIG_FILE"
}

request() {
	local method="$1" url="$2"
	shift 2
	curl --fail-with-body --silent --show-error --request "$method" \
		--header @<(printf 'Authorization: Bearer %s' "$api_token") "$@" "$url"
}

cleanup() {
	if [[ -n "${container_id:-}" ]]; then
		request DELETE "$BASE_URL/v1/cells/$container_id" >/dev/null ||
			log WARN "could not delete the test-owned cell"
	fi
}

main() {
	[[ "$#" -eq 0 ]] || {
		usage >&2
		return 2
	}
	[[ -r "$CONFIG_FILE" ]] || fail "missing readable $CONFIG_FILE"
	api_token="$(env_value PR0XTEUS_API_TOKEN)"
	[[ -n "$api_token" ]] || fail "PR0XTEUS_API_TOKEN is empty"

	curl --fail --silent --show-error --retry "$READY_RETRIES" \
		--retry-connrefused --retry-delay 1 "$METRICS_URL/healthz" >/dev/null
	curl --fail --silent --show-error "$METRICS_URL/metrics" >/dev/null
	local unauthorized_status assignment proxy_url cells direct_ip proxied_ip
	unauthorized_status="$(curl --output /dev/null --silent --write-out '%{http_code}' "$BASE_URL/v1/cells")"
	[[ "$unauthorized_status" == "401" ]] || fail "unauthenticated cells request returned $unauthorized_status"

	assignment="$(request POST "$BASE_URL/v1/proxies" \
		--header 'Content-Type: application/json' --data "{\"country\":\"$TEST_COUNTRY\"}")"
	proxy_url="$(jq -er '.url' <<<"$assignment")"
	if [[ -n "$PROXY_HOST" ]]; then
		proxy_url="${proxy_url/@127.0.0.1:/@${PROXY_HOST}:}"
	fi
	proxy_url="${proxy_url/socks5:/socks5h:}"
	request GET "$BASE_URL/v1/proxies?limit=1&offset=0" |
		jq -e '.proxies | type == "array"' >/dev/null
	request GET "$BASE_URL/v1/pools" | jq -e '.pools | type == "array"' >/dev/null
	cells="$(request GET "$BASE_URL/v1/cells")"
	container_id="$(jq -er '.cells[0].containerId' <<<"$cells")"
	request GET "$BASE_URL/v1/cells/$container_id" |
		jq -e --arg id "$container_id" '.containerId == $id' >/dev/null

	direct_ip="$DIRECT_IP"
	[[ -n "$direct_ip" ]] || fail "PR0XTEUS_TEST_DIRECT_IP is empty"
	[[ -n "$PUBLIC_IP_ADDRESS" ]] || fail "PR0XTEUS_TEST_PUBLIC_IP_ADDRESS is empty"
	proxied_ip="$(curl --ipv4 --fail --silent --show-error --proxy "$proxy_url" \
		--header 'Host: api.ipify.org' \
		"http://$PUBLIC_IP_ADDRESS")"
	[[ "$direct_ip" != "$proxied_ip" ]] || fail "proxy did not change the public IP"

	request DELETE "$BASE_URL/v1/cells/$container_id" >/dev/null
	container_id=""
	log INFO "installed-stack endpoints and real proxy egress passed base_url=$BASE_URL"
}

api_token=""
container_id=""
trap cleanup EXIT
main "$@"
