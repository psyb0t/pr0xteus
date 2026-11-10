# Security

Prefix: SEC. pr0xteus controls Docker and routes traffic through operator WireGuard material, so these hold for every change.

- **SEC1:** Every `/v1` route is wrapped in `APIServer.authenticate`. A new control route goes through it. Only `/healthz` and `/metrics` on the separate metrics listener are unauthenticated, and they expose no credentials.
- **SEC2:** `APIServer` keeps only the SHA-256 digest of the bearer token; `leaseRegistry` keeps only a digest of each lease secret. Never store or return a raw token or lease secret beyond the one allocation response.
- **SEC3:** The API never accepts a file path, image, Docker flag or environment value from a caller. `CellSpawner` builds the mount path from the configured bundle directory and the basename the `Manager` picked, and mounts that one file read-only at `/wgconf/wg0.conf`.
- **SEC4:** The controller never mounts the raw Docker socket. It talks to `docker-socket-proxy` (`PR0XTEUS_DOCKER_HOST`). A new Docker call needs the matching permission in the proxy's environment in `docker-compose.yml` and the `bootstrap/` copy, and nothing broader.
- **SEC5:** Cell privileges are fixed in `spawner.go`: `cap_drop` ALL plus `NET_ADMIN`, `DAC_READ_SEARCH`, `SETUID`, `SETGID`, the `/dev/net/tun` device, `no-new-privileges`, memory, CPU and PID limits, capped logs and no Docker socket. Adding a capability, device, mount or environment variable needs a stated reason in the change, and the docs (`docs/cell.md`, `docs/security.md`) move with it.
- **SEC6:** The cell must never become a clear-net proxy. `cell/entrypoint.sh` keeps the default-drop firewall until a handshake exists, never accepts inbound traffic on `wg0`, and opens the SOCKS5 and control ports only toward the Docker networks. `entrypoint_guard_test.go` guards this.
- **SEC7:** The controller joins `control` and `cell-control` only, never `egress`. `make audit-compose` (`scripts/audit-compose.sh`) enforces this and the rest of the Compose floor: read-only roots, dropped capabilities, `no-new-privileges`, resource and log limits, loopback host ports by default.
- **SEC8:** Real WireGuard files, `pools.yaml`, routing files, `.env` and Tailscale state stay out of Git and out of both image build contexts. `.gitignore`, `.dockerignore` and `.gitleaks.toml` cover them; examples use placeholder names such as `example-node` and `your-tailscale-auth-key`.
- **SEC9:** Bump the Go version in `Dockerfile`, `Dockerfile.dev`, `Dockerfile.integration` and `cell/Dockerfile` together.
