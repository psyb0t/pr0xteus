# pr0xteus — ephemeral wireguard + SOCKS5 worker image.
#
# Orchestrated lazily by pr0xteus.
# One container = one WG tunnel = one upstream SOCKS5 endpoint.
# Caller (the orchestrator) talks SOCKS5 in, traffic comes out the
# WG peer. Kill-switch + DNS-leak-proof from boot to ctx-cancel.
#
# The SOCKS5 endpoint is the first-party cellproxy binary, built from
# this repo's own vendored source (pinned via go.sum), which also
# serves per-cell traffic metrics + health on a control HTTP port.

# ─── builder: the cellproxy binary (metrics-emitting SOCKS5 proxy) ──
FROM golang:1.26.6-alpine@sha256:af8d6740070b8906d12eae1c3e3ea0957fb63f492051ea05e354c38ef9fe88df AS builder

WORKDIR /build
COPY . .
RUN CGO_ENABLED=0 go build -mod=vendor -trimpath \
        -ldflags="-s -w" -o /cellproxy ./cmd/cellproxy

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

COPY --from=builder /cellproxy /usr/local/bin/cellproxy
COPY cell/entrypoint.sh /entrypoint.sh
RUN chmod 0755 /entrypoint.sh /usr/local/bin/cellproxy

# Non-root user for cellproxy. wg-quick + iptables run as root
# (need CAP_NET_ADMIN); entrypoint drops to this UID before exec'ing
# the proxy.
RUN adduser -D -H -s /sbin/nologin -u 1500 proxyuser

# SOCKS5 (1080) + the control HTTP server (9090, health + metrics).
EXPOSE 1080 9090

# Healthcheck: TCP-level handshake on the SOCKS5 port. Note this
# only proves the daemon is up. The controller additionally scrapes
# the control server's /healthz and /status.
HEALTHCHECK --interval=10s --timeout=3s --start-period=15s --retries=3 \
    CMD nc -z 127.0.0.1 1080 || exit 1

ENTRYPOINT ["/entrypoint.sh"]
