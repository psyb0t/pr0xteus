---
title: "Cell image"
description: "The pr0xteus cell: one WireGuard configuration in, one private SOCKS5 listener out. Boot sequence, kill switch, privilege model and image build."
nav_order: 1
parent: "Controller internals"
---

# Cell image

The cell is a disposable worker: one WireGuard configuration in, one private SOCKS5 listener out. It runs `cellproxy`, a first-party Go SOCKS5 proxy built from [`cmd/cellproxy`](https://github.com/psyb0t/pr0xteus/tree/main/cmd/cellproxy) in this repository, which also serves a control HTTP endpoint (`/healthz` and `/status`) reporting per-cell traffic on the private Docker network only. The controller starts the cell lazily and removes it after an idle or unhealthy period.

The image is built from [`cell/Dockerfile`](https://github.com/psyb0t/pr0xteus/blob/main/cell/Dockerfile) and published as `psyb0t/pr0xteus:cell-<version>`.

## Boot sequence

[`cell/entrypoint.sh`](https://github.com/psyb0t/pr0xteus/blob/main/cell/entrypoint.sh) does the networking in this order:

1. Verify the required Docker-provided network sysctl.
2. Read the one mounted WireGuard configuration and validate required fields.
3. Resolve the WireGuard endpoint before tunnel DNS replaces the resolver.
4. Set default-drop IPv4 and IPv6 firewall policy, then allow loopback, established traffic, and only the endpoint UDP handshake.
5. Create `wg0`, pin the endpoint route outside the tunnel, and set the default route through `wg0`.
6. Wait for a real WireGuard handshake until the bounded startup deadline (15 seconds by default).
7. Permit traffic on `wg0`, configure tunnel DNS when provided, and open the SOCKS5 and control ports only to the private Docker network.
8. Start cellproxy (SOCKS5 and control HTTP) as the unprivileged `proxyuser` account.

Failure at any point tears the interface down. The worker must not turn into a normal clear-net proxy when the tunnel is absent.

## Privilege model

The container starts as root because a Linux WireGuard interface, firewall rules, routes, and resolver file need it. This is a deliberate exception, not a default. The controller-spawned cell has only `NET_ADMIN`, `DAC_READ_SEARCH`, `SETUID` and `SETGID` plus the `/dev/net/tun` device, all other capabilities dropped, no Docker socket, and a single read-only WireGuard-file mount. `DAC_READ_SEARCH` lets root read that file, which stays owner-only on the host. `SETUID` and `SETGID` are required only for `su-exec`'s one-way final drop to UID 1500; they do not grant any new host access. Once setup completes, cellproxy runs as UID 1500.

The root filesystem cannot be globally read-only: WireGuard setup creates temporary `/etc/wireguard` and resolver state before the privilege drop. Those files live only for the life of the auto-removed container.

## Traffic counters

`/status` returns the cell ID, parent controller ID, uptime and a traffic snapshot: requests, bytes up and down, active connections, dial failures and the top 50 destinations ranked by bytes. The controller serves it through [`GET /v1/cells`](api.md#get-v1cells).

## Image build

The Dockerfile builds the `cellproxy` binary from this repository's own vendored Go source in a pinned `golang` builder stage, statically linked. Runtime uses a digest-pinned Alpine base with only the WireGuard, firewall, routing, DNS, liveness, and privilege-drop tools needed to boot the worker.

Use `make build-cell`; it is the supported local build entry point and creates `psyb0t/pr0xteus:cell-dev`. A published controller pulls the cell built from its own commit; see [cell image pairing](configuration.md#cell-image-pairing). There is no setting to swap in another cell image.
