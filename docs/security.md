# Security boundaries

- The raw Docker socket is only mounted into `docker-socket-proxy`, never the
  controller or cells.
- The controller is non-root, read-only, capability-empty, resource-capped,
  and log-capped. Its ports default to loopback and can be removed altogether
  for an internal gateway; all-interface publication is explicit.
- Optional tailnet access uses a separate Tailscale sidecar with `/dev/net/tun`, `NET_ADMIN`, and `NET_RAW`. It has its own tailnet identity. Tailscale Serve forwards the authenticated controller API, lease-authenticated SOCKS5 and HTTP proxies, and the unauthenticated health and metrics listener. Restrict access to the metrics port with tailnet policy.
- A cell has the specific WireGuard exception: `NET_ADMIN` and `/dev/net/tun`,
  plus `SETUID`/`SETGID` solely for its one-way final drop to the non-root
  cellproxy account. It begins with default-drop firewall policy, allows the
  WireGuard peer, and does not start cellproxy until a handshake arrives. The
  cellproxy control server is opened only on the internal cell network, never on
  the WireGuard egress side.
- Real configuration and tokens are neither tracked nor included in either
  image build context.
- The controller accepts a strict, size-bounded JSON body and stores only a
  SHA-256 digest of the bearer token after startup.
- Every cell carries a controller scope label, so shutdown and orphan recovery
  only ever act on cells from that controller scope.

These are meaningful boundaries, not magic. Anyone able to modify local pool policy, read the bearer token, or control Docker on the host is already inside the operator trust boundary.

See [deployment](deploy.md) and [API contracts](api.md). Return to the [README](../README.md).
