---
title: "Security boundaries"
description: "What pr0xteus isolates: the Docker socket proxy, controller and cell privileges, token and lease handling, Tailscale exposure and the operator trust boundary."
nav_order: 9
---

# Security boundaries

- The raw Docker socket is only mounted into `docker-socket-proxy`, never the controller or cells. The proxy allows container, image and POST calls and refuses networks, volumes, exec, services, tasks, info and version.
- The controller is non-root, read-only, capability-empty, resource-capped and log-capped. Its ports default to loopback and can be removed altogether for an internal gateway; all-interface publication is explicit. It joins the internal `control` and `cell-control` networks only, never the egress network.
- Optional tailnet access uses a separate Tailscale sidecar with `/dev/net/tun`, `NET_ADMIN`, `NET_RAW` and `DAC_OVERRIDE`. It has its own tailnet identity. Tailscale Serve forwards the authenticated controller API, the lease-authenticated SOCKS5 and HTTP proxies, and the unauthenticated health and metrics listener. Restrict access to the metrics port with tailnet policy.
- A cell has the specific WireGuard exception: `NET_ADMIN` and `/dev/net/tun`, `DAC_READ_SEARCH` to read the bind-mounted WireGuard file, plus `SETUID`/`SETGID` solely for its one-way final drop to the non-root cellproxy account. Every other capability is dropped. It begins with a default-drop firewall policy, allows the WireGuard peer, and does not start cellproxy until a handshake arrives. The cellproxy control server is opened only on the internal cell network, never on the WireGuard egress side. See [cell image](cell.md).
- Real configuration and tokens are neither tracked nor included in either image build context.
- The controller accepts a strict, size-bounded JSON body and stores only a SHA-256 digest of the bearer token after startup. Lease secrets are kept as digests too; the raw secret exists only in the URLs returned to the caller.
- Every cell carries a controller scope label, so shutdown and orphan recovery only ever act on cells from that controller scope.

These are meaningful boundaries, not magic. Anyone able to modify local pool policy, read the bearer token, or control Docker on the host is already inside the operator trust boundary.

See [installation](deploy.md), [controller internals](controller.md) and the [API contracts](api.md).
