# pr0xteus — ephemeral wireguard + SOCKS5 worker image.
#
# Orchestrated lazily by pr0xteus.
# One container = one WG tunnel = one upstream SOCKS5 endpoint.
# Caller (the orchestrator) talks SOCKS5 in, traffic comes out the
# WG peer. Kill-switch + DNS-leak-proof from boot to ctx-cancel.
#
# Base + every package pinned. microsocks pulled at a tagged
# commit SHA so a hijacked maintainer can't slip in a backdoor
# between rebuilds.

# ─── builder: microsocks (single-binary, 50KB) ────────────────────
FROM alpine:3.24.1@sha256:28bd5fe8b56d1bd048e5babf5b10710ebe0bae67db86916198a6eec434943f8b AS builder

ARG MICROSOCKS_TAG=v1.0.5
ARG MICROSOCKS_SHA=98421a21c4adc4c77c0cf3a5d650cc28ad3e0107

RUN apk add --no-cache \
        build-base=0.5-r4 \
        git=2.54.0-r0

WORKDIR /build
RUN git clone --depth=1 --branch=${MICROSOCKS_TAG} \
        https://github.com/rofl0r/microsocks.git . \
    && [ "$(git rev-parse HEAD)" = "${MICROSOCKS_SHA}" ] \
    && make CFLAGS="-O2 -static" LDFLAGS="-static" \
    && strip microsocks

# ─── runtime ─────────────────────────────────────────────────────
FROM alpine:3.24.1@sha256:28bd5fe8b56d1bd048e5babf5b10710ebe0bae67db86916198a6eec434943f8b

# - wireguard-tools: wg-quick + wg
# - iptables / ip6tables: kill switch (block any traffic that
#   doesn't go out wg0; block all IPv6 since WG configs are v4-only)
# - iproute2: ip(8) for routing tweaks wg-quick needs
# - bash: entrypoint runs as bash (set -euo pipefail)
# - bind-tools: dig for hostname → IP resolution at boot (the host
#   resolver supplies DNS; once WG is up resolv.conf is switched)
# - netcat-openbsd: TCP liveness probe for the SOCKS5 listener
RUN apk add --no-cache \
		bash=5.3.9-r1 \
		bind-tools=9.20.26-r0 \
		iproute2=7.0.0-r0 \
		iptables=1.8.13-r0 \
		netcat-openbsd=1.234.1-r0 \
		su-exec=0.3-r0 \
        wireguard-tools=1.0.20260223-r0

COPY --from=builder /build/microsocks /usr/local/bin/microsocks
COPY cell/entrypoint.sh /entrypoint.sh
RUN chmod 0755 /entrypoint.sh /usr/local/bin/microsocks

# Non-root user for microsocks. wg-quick + iptables run as root
# (need CAP_NET_ADMIN); entrypoint drops to this UID before exec'ing
# the proxy.
RUN adduser -D -H -s /sbin/nologin -u 1500 proxyuser

# SOCKS5 listens here. Orchestrator publishes via docker port map.
EXPOSE 1080

# Healthcheck: TCP-level handshake on the SOCKS5 port. Note this
# only proves the daemon is up. The controller additionally checks
# the private SOCKS5 listener before returning its URL.
HEALTHCHECK --interval=10s --timeout=3s --start-period=15s --retries=3 \
    CMD nc -z 127.0.0.1 1080 || exit 1

ENTRYPOINT ["/entrypoint.sh"]
