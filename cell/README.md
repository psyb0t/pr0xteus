# Cell image

The cell is a disposable worker: one WireGuard configuration in, one private
SOCKS5 listener out. The controller starts it lazily and removes it after an
idle or unhealthy period.

## Boot sequence

`entrypoint.sh` deliberately does the networking in this order:

1. Verify the required Docker-provided network sysctl.
2. Read the one mounted WireGuard configuration and validate required fields.
3. Resolve the WireGuard endpoint before tunnel DNS replaces the resolver.
4. Set default-drop IPv4 and IPv6 firewall policy, then allow loopback,
   established traffic, and only the endpoint UDP handshake.
5. Create `wg0`, pin the endpoint route outside the tunnel, and set the
   default route through `wg0`.
6. Wait for a real WireGuard handshake until the bounded startup deadline.
7. Permit traffic on `wg0`, configure tunnel DNS when provided, and open the
   SOCKS5 listener only to the private Docker network.
8. Start microSocks as the unprivileged `proxyuser` account.

Failure at any point tears the interface down. The worker must not turn into a
normal clear-net proxy when the tunnel is absent.

## Privilege model

The container starts as root because a Linux WireGuard interface, firewall
rules, routes, and resolver file need it. This is a deliberate exception, not
a default. The Compose-spawned cell has only `NET_ADMIN`, `SETUID`, and
`SETGID` plus the `/dev/net/tun` device, all other capabilities dropped, no
Docker socket, and a single read-only WireGuard-file mount. `SETUID` and
`SETGID` are required only for `su-exec`'s one-way final drop to UID 1500; they
do not grant any new host access. Once setup completes, microSocks runs as UID
1500.

The root filesystem cannot be globally read-only: WireGuard setup creates
temporary `/etc/wireguard` and resolver state before the privilege drop. Those
files live only for the life of the auto-removed container.

## Image build

`Dockerfile` builds microSocks from a pinned tag and verifies the exact commit
before compiling it statically. Runtime uses a digest-pinned Alpine base with
only the WireGuard, firewall, routing, DNS, liveness, and privilege-drop tools
needed to boot the worker.

Use `make build-cell`; it is the supported local build entry point and creates
`psyb0t/pr0xteus:cell-dev`. A published controller carries its matching cell
tag (`latest` -> `cell-latest`; `vX.Y.Z` -> `cell-vX.Y.Z`). If an operator
overrides that built-in pairing, the replacement image must be pinned by
digest in `.env` rather than supplied as a mutable tag.
