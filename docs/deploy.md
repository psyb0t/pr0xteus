# Deployment guide

Pr0xteus runs from `psyb0t/pr0xteus`, not from a source checkout. You need Linux, Docker, and WireGuard material you are allowed to use. The standard stack keeps API and metrics loopback-only by default, confines raw Docker access to the socket proxy, and keeps WireGuard material plus the bearer token outside Git and image layers.

For the exact working configuration, use [complete-example.md](complete-example.md). For controller/cell internals, see [internal/README.md](../internal/pkg/services/pr0xteus/README.md) and [cell/README.md](../cell/README.md).

Install Docker with the Compose plugin before running the installer.

## Install it

The installer supports per-user and system-wide installation. For a per-user installation without root:

```bash
curl -fsSL https://raw.githubusercontent.com/psyb0t/pr0xteus/main/install.sh | bash
```

That puts the `pr0xteus` command in `~/.local/bin/` and your config in `~/.config/pr0xteus/` (owner-only). If `~/.local/bin` isn't on your `PATH` the installer prints the exact one-liner to add it for bash or zsh.

For a system-wide installation, run it with `sudo` for one shared stack any docker-group user can drive:

```bash
curl -fsSL https://raw.githubusercontent.com/psyb0t/pr0xteus/main/install.sh | sudo bash
```

That puts the command in `/usr/local/bin/` and the config in `/etc/pr0xteus/` (root-owned, readable by the `docker` group). Root gets a system-wide installation; other users get a per-user installation. Use `--system` or `--user` to select the mode explicitly.

Either way it drops managed local Compose templates and starter config, generates a bearer token in an owner-only `.env`, refreshes a readable `.env.example`, and installs the `pr0xteus` command. Later `setup` refreshes only those managed Compose templates. `upgrade` refreshes them and the command, then starts the stack through the refreshed command. `.env`, pools, routing, WireGuard files, and Tailscale state are never replaced. No source checkout required. It pins to the **latest tagged release**, and the controller derives its matching cell image from that tag, so both move together only when you upgrade.

The generated stack runs the controller as the installing operator's UID/GID, not as root. That lets it read the owner-only config mount without weakening your WireGuard-file permissions. `setup` and `upgrade` keep that generated `PR0XTEUS_RUNTIME_USER` value current; do not change it unless the config files are deliberately owned by another local account.

After installing, edit the `.env` in your config directory to change the loopback ports, tune logging, or enable the optional tailnet API.

Want to track `main` instead of a release? Add `--rolling` to force the moving `:latest` image for a single run, on the installer (`… | bash -s -- --rolling`) or on any `pr0xteus start` / `pr0xteus upgrade`.

## Add a WireGuard exit

Copy your provider or private-network file into the config directory:

```bash
cp /wherever/you/keep/your-vpn.conf ~/.config/pr0xteus/secrets/wireguard/us.conf
```

Then edit these two small files so `us` means your file without `.conf` and `US` is the country you want callers to request:

```yaml
# ~/.config/pr0xteus/secrets/pools.yaml
pools:
  us:
    region: north-america
    purpose: private-service-egress
    configs: [us]
    exit_countries:
      us: US
```

```yaml
# ~/.config/pr0xteus/config/egress-routing.yaml
country_to_pool:
  US: us
default_pool: us
```

Start it:

```bash
pr0xteus start
```

The controller stays private on `http://127.0.0.1:8000`; metrics and health stay on `http://127.0.0.1:9091`. Useful commands are deliberately boring:

```bash
pr0xteus status
pr0xteus logs --follow
pr0xteus stop
pr0xteus restart     # restart the stack
pr0xteus upgrade     # re-pin, refresh the command, start the stack, drop the old image
pr0xteus uninstall   # stop the stack, remove the command, ask before deleting data
```

`setup` and `upgrade` refresh `.env.example` and the managed Compose templates while preserving operator config. `upgrade` also refreshes the command, then starts the stack through that new command so current Tailscale routes and public proxy addresses take effect. It re-pins `~/.config/pr0xteus/.env` to the selected image and removes the previous image so dangling layers do not pile up; `uninstall` only deletes your `~/.config/pr0xteus` data and volumes if you say yes at the prompt.

The [complete example](complete-example.md) shows an allocation and an actual host-side egress proof.

## Pull and start a release

```bash
pr0xteus start
```

`pr0xteus start` validates local config before it pulls or starts containers. `latest` uses its baked-in `cell-latest` image. A versioned controller uses its matching versioned cell. To pin a controller release, set `PR0XTEUS_CONTROLLER_IMAGE=psyb0t/pr0xteus:vX.Y.Z` in `~/.config/pr0xteus/.env`, then run `pr0xteus setup` and `pr0xteus start`. The controller pulls that exact matching cell image when it allocates a tunnel; the cell image is not an operator setting.

`pr0xteus upgrade` refreshes the managed base and port-overlay Compose files, refreshes its command, then starts through that refreshed command. No separate `setup` or `start` is needed after an upgrade, including when current Tailscale Serve routes or proxy public addresses changed.

## Tailscale sidecar

To reach the authenticated controller API from your tailnet without binding the controller, metrics, SOCKS5 gateway, or HTTP proxy on the host, add this to the owner-only `~/.config/pr0xteus/.env`:

```dotenv
PR0XTEUS_TAILSCALE_ENABLED=true
PR0XTEUS_DISABLE_HOST_PORTS=true
TS_AUTHKEY=your-tailscale-auth-key
TS_HOSTNAME=pr0xteus
TS_EXTRA_ARGS=--accept-dns=false
```

`pr0xteus start` enables the Compose `tailscale` profile, gives the sidecar its own tailnet identity, and wires Tailscale Serve to the private controller over the Docker `control` network: HTTP on port 80 goes to `pr0xteus:8000`, TCP port 9091 goes to the private metrics listener, TCP port 1080 goes to the controller SOCKS5 gateway, and TCP port 8080 goes to the controller HTTP proxy. The no-host-ports Compose override removes all four controller port publications. When MagicDNS names the node `pr0xteus`, use `http://pr0xteus/v1/...` for the API and returned URLs target `pr0xteus:1080` and `pr0xteus:8080`. Tailscale encrypts every path; the bearer token is still required for API calls and the lease credentials are still required for both proxy protocols.

For a direct Compose deployment, use the same generated config and wire Serve after the sidecar has joined:

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

docker compose --profile tailscale \
  --project-directory "$HOME/.config/pr0xteus" \
  --env-file "$HOME/.config/pr0xteus/.env" \
  -f "$HOME/.config/pr0xteus/docker-compose.yml" \
  -f "$HOME/.config/pr0xteus/docker-compose.no-host-ports.yml" \
  exec -T tailscale tailscale serve --bg --tcp=8080 tcp://pr0xteus:8080
```

For direct Compose, set `PR0XTEUS_SOCKS_PUBLIC_ADDRESS` and `PR0XTEUS_HTTP_PROXY_PUBLIC_ADDRESS` to the sidecar's MagicDNS host with ports `1080` and `8080`, then recreate `pr0xteus` so future leases use reachable addresses. The `pr0xteus` wrapper performs those steps itself.

The sidecar never publishes a host port and does not reuse a Tailscale client running on the host. Its persistent state is `~/.config/pr0xteus/tailscale/state`, so restarts retain the sidecar identity and do not consume the auth key again. For Headscale, set `TS_EXTRA_ARGS=--accept-dns=false --login-server=https://headscale.example`. The sidecar needs `/dev/net/tun`, `NET_ADMIN`, and `NET_RAW` to create its own kernel-mode tunnel. WireGuard cells separately need `/dev/net/tun` and `NET_ADMIN` for their exit tunnels. Append Tailscale options such as `--advertise-tags=tag:your-tag` to `TS_EXTRA_ARGS` when your tailnet policy requires them. An ephemeral auth key can create a node that disappears after going offline; use a persistent identity for a stable gateway hostname. Keep tailnet access to port 9091 restricted because its health and Prometheus endpoints are deliberately unauthenticated.

Leave `PR0XTEUS_DISABLE_HOST_PORTS=false` for the default loopback deployment. While it is false, the `PR0XTEUS_*_HOST_PORT` values are complete `HOST:PORT` mappings that default to `127.0.0.1`. Set one to `0.0.0.0:PORT` only when an authenticated private network boundary protects it. Keep both `*_PUBLIC_ADDRESS` values set to real reachable addresses for allocated proxy URLs; never use `0.0.0.0` for either value.

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

The response contains short-lived `socks5://...` and `http://...` controller lease URLs. They work from the host or another trusted client that can reach the controller. In Tailscale-only mode, the wrapper returns URLs with the sidecar's MagicDNS host on ports 1080 and 8080, so make the authenticated API call and use the lease from another tailnet machine. Do not publish a cell port or expose the controller API beyond an authenticated private boundary.

## Operations

```bash
pr0xteus status
pr0xteus logs --tail=200
pr0xteus restart
pr0xteus upgrade
pr0xteus start
```

The controller and socket proxy use capped JSON logs. Use `pr0xteus restart` to restart the stack. For configuration or provider changes, use either command.

Source checkout is development-only. Its Makefile runs format, lint, and all tests in the development container; it is not required to operate the image.
