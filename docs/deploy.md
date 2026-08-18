# Deployment guide

Pr0xteus runs from `psyb0t/pr0xteus`, not from a source checkout. You need
Linux, Docker, and WireGuard material you are allowed to use. The standard
stack keeps API and metrics loopback-only by default, confines raw Docker
access to the socket proxy, and keeps WireGuard material plus the bearer token
outside Git and image layers.

For the exact working configuration, use
[complete-example.md](complete-example.md). For controller/cell internals, see
[internal/README.md](../internal/pkg/services/pr0xteus/README.md) and [cell/README.md](../cell/README.md).

## Install it

```bash
curl -fsSL https://raw.githubusercontent.com/psyb0t/pr0xteus/main/install.sh | bash
```

This guide uses the **per-user** install (no root): the command lands in
`~/.local/bin` and config in `~/.config/pr0xteus`. For a shared, system-wide
stack — `sudo bash` → `/usr/local/bin` + `/etc/pr0xteus`, readable by the
`docker` group — see the [README](../README.md); the paths below then live under
`/etc/pr0xteus` instead of `~/.config/pr0xteus`.

The installer creates `~/.config/pr0xteus/`, writes managed local Compose
templates, and adds the `pr0xteus` command. A later installer run, `setup`, or
`upgrade` refreshes only those templates and `.env.example`; it preserves
`.env`, pools, routing, WireGuard files, and Tailscale state. It creates pool
and routing skeletons plus empty `secrets/wireguard/`; it never manufactures a
provider configuration. The random bearer token is written directly to
owner-only `~/.config/pr0xteus/.env` as `PR0XTEUS_API_TOKEN`.

The generated `PR0XTEUS_RUNTIME_USER` is the installing operator's UID:GID.
Compose runs the controller with that identity so it can read the same
owner-only config mount that holds the WireGuard material. `setup` and
`upgrade` refresh that generated value; leave it alone unless you intentionally
change the config directory owner.

Add real `*.conf` files under `~/.config/pr0xteus/secrets/wireguard/`, then change
`~/.config/pr0xteus/secrets/pools.yaml` and
`~/.config/pr0xteus/config/egress-routing.yaml` to reference their basenames. The
installer handles the absolute host path Docker needs when it binds one chosen
file into each cell.

## Pull and start a release

```bash
pr0xteus start
```

`pr0xteus start` validates local config before it pulls or starts containers.
`latest` uses its baked-in `cell-latest` image. A versioned controller uses its
matching versioned cell. To pin a controller release, set
`PR0XTEUS_CONTROLLER_IMAGE=psyb0t/pr0xteus:vX.Y.Z` in
`~/.config/pr0xteus/.env`, then run `pr0xteus setup` and `pr0xteus start`.
The controller pulls that exact matching cell image when it allocates a tunnel;
the cell image is not an operator setting.

After upgrading to a release with new runtime wiring, run `pr0xteus setup`.
It refreshes the managed base and port-overlay Compose files while preserving
your existing operator configuration.

## Tailscale sidecar

To reach the authenticated controller API from your tailnet without binding
the controller, metrics, or SOCKS gateway on the host, add this to the
owner-only `~/.config/pr0xteus/.env`:

```dotenv
PR0XTEUS_TAILSCALE_ENABLED=true
PR0XTEUS_DISABLE_HOST_PORTS=true
TS_AUTHKEY=your-tailscale-auth-key
TS_HOSTNAME=pr0xteus
TS_EXTRA_ARGS=--accept-dns=false
```

`pr0xteus start` enables the Compose `tailscale` profile, gives the sidecar its
own tailnet identity, and wires Tailscale Serve to the private controller over
the Docker `control` network: HTTP on port 80 goes to `pr0xteus:8000`, TCP port
9091 goes to the private metrics listener, and TCP port 1080 goes to the
controller SOCKS gateway. The no-host-ports Compose
override removes all three controller port publications. When MagicDNS names
the node `pr0xteus`, use `http://pr0xteus/v1/...` for the API and the returned
lease URL targets `pr0xteus:1080`. Tailscale encrypts both paths; the bearer
token is still required for API calls and the lease credentials are still
required for SOCKS.

For a direct Compose deployment, use the same generated config and wire Serve
after the sidecar has joined:

```bash
docker compose --profile tailscale \
  --project-directory "$HOME/.config/pr0xteus" \
  --env-file "$HOME/.config/pr0xteus/.env" \
  -f "$HOME/.config/pr0xteus/docker-compose.yml" \
  -f "$HOME/.config/pr0xteus/docker-compose.no-host-ports.yml" \
  up --detach --pull always

docker compose --profile tailscale \
  --project-directory "$HOME/.config/pr0xteus" \
  --env-file "$HOME/.config/pr0xteus/.env" \
  -f "$HOME/.config/pr0xteus/docker-compose.yml" \
  -f "$HOME/.config/pr0xteus/docker-compose.no-host-ports.yml" \
  exec -T tailscale tailscale serve --bg --http=80 http://pr0xteus:8000

docker compose --profile tailscale \
  --project-directory "$HOME/.config/pr0xteus" \
  --env-file "$HOME/.config/pr0xteus/.env" \
  -f "$HOME/.config/pr0xteus/docker-compose.yml" \
  -f "$HOME/.config/pr0xteus/docker-compose.no-host-ports.yml" \
  exec -T tailscale tailscale serve --bg --tcp=9091 tcp://pr0xteus:9091

docker compose --profile tailscale \
  --project-directory "$HOME/.config/pr0xteus" \
  --env-file "$HOME/.config/pr0xteus/.env" \
  -f "$HOME/.config/pr0xteus/docker-compose.yml" \
  -f "$HOME/.config/pr0xteus/docker-compose.no-host-ports.yml" \
  exec -T tailscale tailscale serve --bg --tcp=1080 tcp://pr0xteus:1080
```

For direct Compose, set `PR0XTEUS_SOCKS_PUBLIC_ADDRESS` to the sidecar's
MagicDNS host plus `:1080`, then recreate `pr0xteus` so future leases use that
reachable address. The `pr0xteus` wrapper performs those two steps itself.

The sidecar never publishes a host port and does not reuse a Tailscale client
running on the host. Its persistent state is
`~/.config/pr0xteus/tailscale/state`, so restarts retain the sidecar identity and do
not consume the auth key again. For Headscale, set
`TS_EXTRA_ARGS=--accept-dns=false --login-server=https://headscale.example`.
The sidecar needs `/dev/net/tun`, `NET_ADMIN`, and `NET_RAW` to create its own
kernel-mode tunnel; no other Pr0xteus service receives those privileges.
Keep tailnet access to port 9091 restricted because its health and Prometheus
endpoints are deliberately unauthenticated.

Leave `PR0XTEUS_DISABLE_HOST_PORTS=false` for the default loopback deployment.
While it is false, the `PR0XTEUS_*_HOST_PORT` values are complete `HOST:PORT`
mappings that default to `127.0.0.1`. Set one to `0.0.0.0:PORT` only when an
authenticated private network boundary protects it. Keep
`PR0XTEUS_SOCKS_PUBLIC_ADDRESS` set to a real reachable address for allocated
SOCKS URLs; never use `0.0.0.0` for that value.

## Verify the default host-local deployment without leaking the bearer token

```bash
token="$(sed -n 's/^PR0XTEUS_API_TOKEN=//p' ~/.config/pr0xteus/.env)"
curl --fail-with-body --request POST \
  --header @<(printf 'Authorization: Bearer %s' "$token") \
  --header 'Content-Type: application/json' \
  --data '{"country":"US"}' \
  http://127.0.0.1:8000/v1/proxies
unset token
```

The returned `socks5://...` URL is a short-lived controller SOCKS gateway
lease and works from the host or another trusted client that can reach the
gateway. The controller forwards it to the selected cell through the internal
cell-control network. In Tailscale-only mode, the wrapper returns a lease URL
with the sidecar's MagicDNS host on port 1080, so make the authenticated API
call and use the lease from another tailnet machine. Do not publish a cell port
or expose the controller API beyond an authenticated private boundary.

## Operations

```bash
pr0xteus status
pr0xteus logs --tail=200
pr0xteus restart
pr0xteus upgrade
pr0xteus start
```

The controller and socket proxy use capped JSON logs. Use `pr0xteus restart`
to restart the stack. For configuration or provider changes, use either command.

Source checkout is development-only. Its Makefile runs format, lint, and all
tests in the development container; it is not required to operate the image.
