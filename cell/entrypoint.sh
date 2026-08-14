#!/bin/bash
# pr0xteus entrypoint — bring up a single WG tunnel + SOCKS5 proxy,
# leak-proof from boot to ctx-cancel.
#
# Order (every step matters):
#   1. Verify sysctls were passed via docker --sysctl. We do NOT try
#      to write /proc/sys ourselves; docker only exposes the
#      container's net.* namespace as read-only.
#   2. Iptables default-DROP on every chain. Nothing escapes before
#      the tunnel is up.
#   3. Allow loopback + conntrack ESTABLISHED.
#   4. Resolve Endpoint hostname → IP via the HOST resolver (DNS
#      hasn't been switched yet). Add a /32 route via eth0 for that
#      IP so the WG handshake doesn't go back through wg0 (loop).
#   5. Bring up wg0 with `ip link` + `wg setconf` directly (NOT
#      wg-quick — wg-quick insists on sysctl writes that fail).
#   6. Allow all on wg0; allow handshake to the endpoint IP/port.
#   7. Open the SOCKS5 port on eth0 for the configured Docker network.
#   8. Switch /etc/resolv.conf to the WG-supplied DNS.
#   9. Drop privileges and start microsocks.
#
# Signal handler kills microsocks + tears down wg0 cleanly so the
# orchestrator's reaper doesn't leave half-up kernel state behind.

set -euo pipefail

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

trap 'log ERROR "entrypoint failed"; cleanup; exit 1' ERR

WG_CONF="/etc/wireguard/wg0.conf"
SRC_CONF="/wgconf/wg0.conf"
SOCKS5_PORT="${PR0XTEUS_SOCKS5_PORT:-1080}"
SOCKS5_BIND="${PR0XTEUS_SOCKS5_BIND:-0.0.0.0}"
HANDSHAKE_WAIT_SECONDS="${PR0XTEUS_HANDSHAKE_WAIT_SECONDS:-15}"

cleanup() {
	log INFO "tearing down tunnel interface"
	# microsocks and the interface may already be gone during a failed boot.
	pkill -TERM microsocks 2>/dev/null || true   # intentional: proxy may not have started
	ip link set dev wg0 down 2>/dev/null || true # intentional: interface may not exist
	ip link delete dev wg0 2>/dev/null || true   # intentional: interface may already be gone
}

shutdown() {
	log INFO "shutdown signal received"
	cleanup
	exit 0
}
trap shutdown TERM INT

# ─── 1. Verify sysctls (must be set via docker --sysctl) ─────────
SRC_VALID=$(cat /proc/sys/net/ipv4/conf/all/src_valid_mark 2>/dev/null || echo "0")
if [[ "${SRC_VALID}" != "1" ]]; then
	log ERROR "required src_valid_mark sysctl is missing"
	exit 1
fi
log DEBUG "required src_valid_mark sysctl is present"

# ─── prepare wg0.conf ────────────────────────────────────────────
if [[ ! -r "${SRC_CONF}" ]]; then
	log ERROR "required WireGuard configuration is missing"
	exit 1
fi

mkdir -p /etc/wireguard
cp "${SRC_CONF}" "${WG_CONF}"
chmod 0600 "${WG_CONF}"

# Pull the fields we need out of the conf. wg-quick syntax is INI
# with extensions; the worker supports the single-peer subset standard
# WireGuard clients emit.
WG_PRIVATE_KEY=$(awk -F' = ' '/^PrivateKey/{print $2}' "${WG_CONF}" | tr -d ' ')
WG_ADDRESS=$(awk -F' = ' '/^Address/{print $2}' "${WG_CONF}" | tr -d ' ' | head -1)
WG_DNS_RAW=$(awk -F' = ' '/^DNS/{print $2}' "${WG_CONF}" | tr -d ' ')
WG_MTU=$(awk -F' = ' '/^MTU/{print $2}' "${WG_CONF}" | tr -d ' ')
WG_MTU="${WG_MTU:-1420}"
WG_PEER_PUBKEY=$(awk -F' = ' '/^PublicKey/{print $2}' "${WG_CONF}" | tr -d ' ')
WG_PEER_ALLOWED=$(awk -F' = ' '/^AllowedIPs/{print $2}' "${WG_CONF}" | tr -d ' ')
WG_ENDPOINT=$(awk -F' = ' '/^Endpoint/{print $2}' "${WG_CONF}" | tr -d ' ')
WG_ENDPOINT_HOST="${WG_ENDPOINT%:*}"
WG_ENDPOINT_PORT="${WG_ENDPOINT##*:}"

for v in WG_PRIVATE_KEY WG_ADDRESS WG_PEER_PUBKEY WG_PEER_ALLOWED \
	WG_ENDPOINT_HOST WG_ENDPOINT_PORT; do
	if [[ -z "${!v}" ]]; then
		log ERROR "required WireGuard configuration field is missing"
		exit 1
	fi
done

if [[ ! "${HANDSHAKE_WAIT_SECONDS}" =~ ^[1-9][0-9]*$ ]]; then
	log ERROR "handshake wait must be a positive integer"
	exit 1
fi

# Resolve endpoint hostname via the (pre-tunnel) host resolver.
if [[ "${WG_ENDPOINT_HOST}" =~ ^[0-9.]+$ ]]; then
	WG_ENDPOINT_IP="${WG_ENDPOINT_HOST}"
else
	WG_ENDPOINT_IP=$(dig +short +time=3 +tries=2 "${WG_ENDPOINT_HOST}" | sed -n '1p')
fi
if [[ -z "${WG_ENDPOINT_IP}" ]]; then
	log ERROR "could not resolve WireGuard endpoint"
	exit 1
fi
log DEBUG "WireGuard endpoint resolved"

EGRESS_IF=$(ip route show default | awk 'NR == 1 {value = $5} END {print value}')
EGRESS_IF="${EGRESS_IF:-eth0}"
EGRESS_GW=$(ip route show default | awk 'NR == 1 {value = $3} END {print value}')
log DEBUG "host egress interface selected"

# ─── 2. Default DROP on every chain ──────────────────────────────
log INFO "installing tunnel kill-switch"
iptables -P INPUT DROP
iptables -P OUTPUT DROP
iptables -P FORWARD DROP

# IPv6 is disabled through Docker sysctls; hosts without ip6tables reject these.
ip6tables -P INPUT DROP 2>/dev/null || true   # intentional: IPv6 may be unavailable
ip6tables -P OUTPUT DROP 2>/dev/null || true  # intentional: IPv6 may be unavailable
ip6tables -P FORWARD DROP 2>/dev/null || true # intentional: IPv6 may be unavailable

# ─── 3. Allow loopback + established ─────────────────────────────
iptables -A INPUT -i lo -j ACCEPT
iptables -A OUTPUT -o lo -j ACCEPT
iptables -A INPUT -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT
iptables -A OUTPUT -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT

# ─── 4. WG handshake outbound on eth0 to the resolved endpoint ──
iptables -A OUTPUT -o "${EGRESS_IF}" \
	-d "${WG_ENDPOINT_IP}" -p udp --dport "${WG_ENDPOINT_PORT}" \
	-j ACCEPT

# Pin a /32 route to the endpoint via eth0 BEFORE wg0 default is
# installed, otherwise the handshake packet would try to leave via
# wg0 itself (which is the tunnel ... carried by the same packet).
if [[ -n "${EGRESS_GW}" ]]; then
	# An existing route is already correct, so duplicate-add failure is harmless.
	ip route add "${WG_ENDPOINT_IP}/32" via "${EGRESS_GW}" \
		dev "${EGRESS_IF}" 2>/dev/null || true # intentional: route may already exist
fi

# ─── 5. Bring up wg0 manually (skips wg-quick's sysctl write) ────
log INFO "bringing up tunnel interface"

# Strip the wg(8) setconf-friendly conf (drop [Interface] keys it
# doesn't accept; drop the Endpoint hostname so wg uses IP only).
STRIPPED=$(mktemp)
cat >"${STRIPPED}" <<EOF
[Interface]
PrivateKey = ${WG_PRIVATE_KEY}

[Peer]
PublicKey = ${WG_PEER_PUBKEY}
AllowedIPs = ${WG_PEER_ALLOWED}
Endpoint = ${WG_ENDPOINT_IP}:${WG_ENDPOINT_PORT}
PersistentKeepalive = 25
EOF

ip link add wg0 type wireguard
wg setconf wg0 "${STRIPPED}"
rm -f "${STRIPPED}"

ip address add "${WG_ADDRESS}" dev wg0
ip link set mtu "${WG_MTU}" up dev wg0

# Default route via wg0 — replace (not add) so it takes over from
# the eth0 default that docker installed. The /32 we pinned above
# keeps endpoint traffic on eth0.
ip route replace default dev wg0

wait_for_handshake() {
	local deadline handshake
	deadline=$((SECONDS + HANDSHAKE_WAIT_SECONDS))

	while ((SECONDS < deadline)); do
		handshake=$(wg show wg0 latest-handshakes | awk 'NR == 1 {value = $2} END {print value}')
		if [[ -n "${handshake}" && "${handshake}" != "0" ]]; then
			log INFO "WireGuard handshake established"
			return 0
		fi

		sleep 1
	done

	log ERROR "WireGuard handshake did not arrive before the startup deadline"
	return 1
}

wait_for_handshake

# ─── 6. Allow everything on wg0 ─────────────────────────────────
iptables -A OUTPUT -o wg0 -j ACCEPT
iptables -A INPUT -i wg0 -j ACCEPT

# ─── 7. Inbound SOCKS5 from the docker port map ────────────────
iptables -A INPUT -i "${EGRESS_IF}" \
	-p tcp --dport "${SOCKS5_PORT}" -j ACCEPT

# ─── 8. Resolv.conf points at WG DNS only (no DNS leak) ────────
if [[ -n "${WG_DNS_RAW}" ]]; then
	: >/etc/resolv.conf
	while IFS= read -r dns_ip; do
		printf 'nameserver %s\n' "${dns_ip}" >>/etc/resolv.conf
	done < <(tr ',' '\n' <<<"${WG_DNS_RAW}")
	log INFO "configured tunnel DNS"
fi

# ─── 9. Drop privileges + start microsocks ────────────────────────
log INFO "starting SOCKS5 listener"

su-exec proxyuser \
	microsocks -i "${SOCKS5_BIND}" -p "${SOCKS5_PORT}" &
MS_PID=$!

# Wait for microsocks; signals (TERM/INT) hit the trap above and
# call cleanup() before we exit.
wait "${MS_PID}"
