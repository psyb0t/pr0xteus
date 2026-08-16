#!/bin/bash
# pr0xteus entrypoint — bring up a single WG tunnel + SOCKS5 proxy,
# leak-proof from boot to ctx-cancel.
#
# Order (every step matters):
#   1. Verify sysctls were passed via docker --sysctl. We do NOT try
#      to write /proc/sys ourselves; docker only exposes the
#      container's net.* namespace as read-only.
#      Then parse wg0.conf and resolve the Endpoint hostname → IP via
#      the HOST resolver. This resolution necessarily runs BEFORE the
#      kill-switch: we can't pin the endpoint route without the IP,
#      and it's safe because no proxy listener exists yet — only this
#      bootstrap lookup leaves the box, never user/proxied traffic.
#   2. Iptables default-DROP on every chain — the kill-switch. From
#      here nothing escapes except the pinned WireGuard endpoint.
#   3. Allow loopback + conntrack ESTABLISHED,RELATED.
#   4. Allow the handshake out to the endpoint IP/port on eth0 and pin
#      a /32 route via eth0 so the handshake doesn't loop back through
#      wg0 (the tunnel it is establishing).
#   5. Bring up wg0 with `ip link` + `wg setconf` directly (NOT
#      wg-quick — wg-quick insists on sysctl writes that fail), then
#      wait for a real handshake before proceeding.
#   6. Allow egress OUT via wg0. Return traffic is already covered by
#      ESTABLISHED,RELATED; wg0 never accepts a NEW inbound connection,
#      so cellproxy's SOCKS5 + control ports stay unreachable from the
#      tunnel (egress) side.
#   7. Accept new inbound: SOCKS5 on the egress interface for intentional
#      direct Docker consumers and on the internal control interface for the
#      controller gateway; the control port stays on the control interface.
#   8. Switch /etc/resolv.conf to the WG-supplied DNS.
#   9. Drop privileges and start cellproxy.
#
# Signal handler kills cellproxy + tears down wg0 cleanly so the
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
# SOCKS5 bind/port and the control port are read from the environment by
# cellproxy itself; the entrypoint only needs the ports for the iptables rules.
SOCKS5_PORT="${PR0XTEUS_SOCKS5_PORT:-1080}"
CONTROL_PORT="${PR0XTEUS_CELL_CONTROL_PORT:-9090}"
HANDSHAKE_WAIT_SECONDS="${PR0XTEUS_HANDSHAKE_WAIT_SECONDS:-15}"

cleanup() {
	log INFO "tearing down tunnel interface"
	# cellproxy and the interface may already be gone during a failed boot.
	pkill -TERM cellproxy 2>/dev/null || true    # intentional: proxy may not have started
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

# The control interface is the cell's second docker network — the internal
# cell-control net, present when the controller runs in dual-network mode: the
# first IPv4-addressed interface that is neither loopback nor the egress iface
# (wg0 does not exist yet here). Empty means single-network mode, where the
# control port shares the egress interface. awk consumes all input (no early
# exit) so pipefail never sees a SIGPIPE on the upstream `ip`.
CONTROL_IF=$(ip -o -4 addr show | awk -v egress="$EGRESS_IF" \
	'$2 != "lo" && $2 != egress && !found { print $2; found = 1 }')
CONTROL_IF="${CONTROL_IF:-$EGRESS_IF}"
log DEBUG "cell network interfaces selected"

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

# ─── 6. Allow egress OUT via the tunnel ─────────────────────────
# OUTPUT via wg0 only. We deliberately do NOT add a blanket
# `-A INPUT -i wg0 -j ACCEPT`: return traffic for the proxy's own
# outbound flows is already covered by the ESTABLISHED,RELATED accept
# above, and accepting NEW inbound on wg0 would expose cellproxy's
# SOCKS5 + control ports to the tunnel (egress) side — the VPN peer or
# provider. New inbound is accepted only on eth0, below.
iptables -A OUTPUT -o wg0 -j ACCEPT

# ─── 7. Inbound SOCKS5 + control HTTP from the docker networks ──
# SOCKS5 is accepted on the egress interface for direct Docker-network users and
# on the internal controller network for the controller-fronted gateway. The
# control server (/healthz + /status) stays on that internal network. New
# inbound is never accepted on wg0, so neither service is exposed through the
# WireGuard exit.
iptables -A INPUT -i "${EGRESS_IF}" \
	-p tcp --dport "${SOCKS5_PORT}" -j ACCEPT
iptables -A INPUT -i "${CONTROL_IF}" \
	-p tcp --dport "${SOCKS5_PORT}" -j ACCEPT
iptables -A INPUT -i "${CONTROL_IF}" \
	-p tcp --dport "${CONTROL_PORT}" -j ACCEPT

# ─── 8. Resolv.conf points at WG DNS only (no DNS leak) ────────
if [[ -n "${WG_DNS_RAW}" ]]; then
	: >/etc/resolv.conf
	while IFS= read -r dns_ip; do
		printf 'nameserver %s\n' "${dns_ip}" >>/etc/resolv.conf
	done < <(tr ',' '\n' <<<"${WG_DNS_RAW}")
	log INFO "configured tunnel DNS"
fi

# ─── 9. Drop privileges + start cellproxy ─────────────────────────
log INFO "starting cellproxy SOCKS5 + control server"

# cellproxy reads PR0XTEUS_SOCKS5_BIND/PORT and PR0XTEUS_CELL_CONTROL_*
# (plus PR0XTEUS_PARENT_ID) from the environment the orchestrator set;
# su-exec preserves that environment when it drops privileges.
su-exec proxyuser cellproxy &
PROXY_PID=$!

# Wait for cellproxy; signals (TERM/INT) hit the trap above and
# call cleanup() before we exit.
wait "${PROXY_PID}"
