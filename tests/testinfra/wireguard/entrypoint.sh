#!/bin/sh
set -eu

cleanup() {
	ip link delete wg0 2>/dev/null || true
}

trap 'cleanup; exit 0' TERM INT

umask 077
server_private_key="$(wg genkey)"
server_public_key="$(printf '%s' "$server_private_key" | wg pubkey)"
client_private_key="$(wg genkey)"
client_public_key="$(printf '%s' "$client_private_key" | wg pubkey)"
server_key_file="$(mktemp)"
printf '%s' "$server_private_key" > "$server_key_file"

ip link add wg0 type wireguard
ip address add 10.251.0.1/24 dev wg0
wg set wg0 \
	listen-port 51820 \
	private-key "$server_key_file" \
	peer "$client_public_key" \
	allowed-ips 10.251.0.2/32
rm -f "$server_key_file"
ip link set wg0 up

iptables -A FORWARD -i wg0 -o eth0 -j ACCEPT
iptables -A FORWARD -i eth0 -o wg0 -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT
iptables -t nat -A POSTROUTING -o eth0 -j MASQUERADE

mkdir -p /srv
printf '%s\n' 'ok' > /srv/healthz
/usr/sbin/httpd -f -p 8080 -h /srv &

cat > /shared/integration.conf <<EOF
[Interface]
PrivateKey = ${client_private_key}
Address = 10.251.0.2/32

[Peer]
PublicKey = ${server_public_key}
AllowedIPs = 0.0.0.0/0
Endpoint = wireguard-peer:51820
PersistentKeepalive = 25
EOF
chmod 0444 /shared/integration.conf

printf '%s\n' 'test WireGuard peer ready'

while :; do
	sleep 600 &
	wait "$!"
done
