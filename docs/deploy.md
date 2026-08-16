# Deployment guide

Pr0xteus runs from `psyb0t/pr0xteus`, not from a source checkout. You need
Linux, Docker, and WireGuard material you are allowed to use. The standard
stack keeps API and metrics loopback-only, confines raw Docker access to the
socket proxy, and keeps WireGuard material plus the bearer token outside Git
and image layers.

For the exact working configuration, use
[complete-example.md](complete-example.md). For controller/cell internals, see
[internal/README.md](../internal/README.md) and [cell/README.md](../cell/README.md).

## Install it

```bash
curl -fsSL https://raw.githubusercontent.com/psyb0t/pr0xteus/main/install.sh | sudo bash
```

The installer creates `~/.config/pr0xteus/`, writes the local `docker-compose.yml`,
and adds the `pr0xteus` command. It preserves config on a later run. It creates
`.env`, pool and routing skeletons, and empty `secrets/wireguard/`; it never
manufactures a provider configuration. The random bearer token is written
directly to owner-only `~/.config/pr0xteus/.env` as `PR0XTEUS_API_TOKEN`.

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
`~/.config/pr0xteus/.env`, then run `pr0xteus setup` and `pr0xteus start`. A deliberate
`PR0XTEUS_CELL_IMAGE` override must be a released digest and leave
`PR0XTEUS_ALLOW_UNPINNED_CELL_IMAGE=false`.

## Tailscale sidecar

The controller and metrics listeners stay on host loopback. To reach the
authenticated controller API from your tailnet, add this to the owner-only
`~/.config/pr0xteus/.env`:

```dotenv
PR0XTEUS_TAILSCALE_ENABLED=true
TS_AUTHKEY=your-tailscale-auth-key
TS_HOSTNAME=pr0xteus
TS_EXTRA_ARGS=--accept-dns=false
```

`pr0xteus start` enables the Compose `tailscale` profile, gives the sidecar its
own tailnet identity, and wires Tailscale Serve to `http://pr0xteus:8000` over
the private Docker control network. The tailnet URL is
`http://pr0xteus/v1/...` when MagicDNS names the node `pr0xteus`. Tailscale
encrypts that path; the bearer token is still required for API calls.

For a direct Compose deployment, use the same generated config and wire Serve
after the sidecar has joined:

```bash
docker compose --profile tailscale \
  --project-directory "$HOME/.config/pr0xteus" \
  --env-file "$HOME/.config/pr0xteus/.env" \
  -f "$HOME/.config/pr0xteus/docker-compose.yml" \
  up --detach --pull always

docker compose --profile tailscale \
  --project-directory "$HOME/.config/pr0xteus" \
  --env-file "$HOME/.config/pr0xteus/.env" \
  -f "$HOME/.config/pr0xteus/docker-compose.yml" \
  exec -T tailscale tailscale serve --bg --http=80 http://pr0xteus:8000
```

The sidecar never publishes a host port and does not reuse a Tailscale client
running on the host. Its persistent state is
`~/.config/pr0xteus/tailscale/state`, so restarts retain the sidecar identity and do
not consume the auth key again. For Headscale, set
`TS_EXTRA_ARGS=--accept-dns=false --login-server=https://headscale.example`.
The sidecar needs `/dev/net/tun`, `NET_ADMIN`, and `NET_RAW` to create its own
kernel-mode tunnel; no other Pr0xteus service receives those privileges.

## Verify without leaking the bearer token

```bash
token="$(sed -n 's/^PR0XTEUS_API_TOKEN=//p' ~/.config/pr0xteus/.env)"
curl --fail-with-body \
  --header @<(printf 'Authorization: Bearer %s' "$token") \
  --header 'Content-Type: application/json' \
  --data '{"country":"US"}' \
  http://127.0.0.1:8000/v1/proxies
unset token
```

The returned `socks5://...` URL works from an authorized container attached to
`pr0xteus-egress`, not from the host. Do not publish a cell port or expose the
controller API beyond an authenticated private boundary.

## Operations

```bash
pr0xteus status
pr0xteus logs --tail=200
pr0xteus upgrade
pr0xteus start
```

The controller and socket proxy use capped JSON logs. For configuration or
provider changes, run `pr0xteus start`; it checks config before starting.

Source checkout is development-only. Its Makefile runs format, lint, and all
tests in the development container; it is not required to operate the image.
