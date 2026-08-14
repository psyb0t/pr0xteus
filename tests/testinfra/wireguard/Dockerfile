# Real WireGuard peer used only by the Testcontainers integration stack.
FROM alpine:3.20.6@sha256:de4fe7064d8f98419ea6b49190df1abbf43450c1702eeb864fe9ced453c1cc5f

RUN apk add --no-cache \
	busybox-extras=1.36.1-r31 \
	iptables=1.8.10-r3 \
	iproute2=6.9.0-r0 \
	wireguard-tools=1.0.20210914-r4

COPY tests/testinfra/wireguard/entrypoint.sh /entrypoint.sh
RUN chmod 0755 /entrypoint.sh

ENTRYPOINT ["/entrypoint.sh"]
