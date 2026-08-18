# Complete image-first setup and a real SOCKS5 proof

This is the operator path: install the published image, add your own authorized
WireGuard configuration, start the private stack, then allocate one SOCKS5
exit. You need Linux and Docker; `docker compose` is already part of Docker.
pr0xteus does not create a VPN account or fetch provider configuration for you.

For component detail, see [architecture.md](architecture.md),
[api.md](api.md), [internal/README.md](../internal/pkg/services/pr0xteus/README.md), and
[cell/README.md](../cell/README.md).

## 1. Install it

```bash
curl -fsSL https://raw.githubusercontent.com/psyb0t/pr0xteus/main/install.sh | bash
```

This walkthrough uses the **per-user** install (no root): the command lands in
`~/.local/bin` and config in `~/.config/pr0xteus`. For a shared, system-wide
stack — `sudo bash` → `/usr/local/bin` + `/etc/pr0xteus`, readable by the
`docker` group — see the [README](../README.md).

The installer creates `~/.config/pr0xteus/`, writes its local `docker-compose.yml`,
and installs the `pr0xteus` command. It only creates these local files when
they are missing:

```text
~/.config/pr0xteus/secrets/wireguard/           put real *.conf files here
~/.config/pr0xteus/secrets/pools.yaml            approved logical pools
~/.config/pr0xteus/config/egress-routing.yaml    requested country -> pool mapping
~/.config/pr0xteus/.env                          bearer token, host path, image and ports
```

`.env` is mode `0600`; it contains `PR0XTEUS_API_TOKEN`. Keep it local. There
is no separate Compose `secrets/` token file.

The controller carries its matching cell reference: `latest` carries
`cell-latest`; `vX.Y.Z` carries `cell-vX.Y.Z`. To pin a release, set
`PR0XTEUS_CONTROLLER_IMAGE=psyb0t/pr0xteus:vX.Y.Z` in `~/.config/pr0xteus/.env`, then
run `pr0xteus setup`. The controller pulls the matching cell on demand; there
is no cell-image override.

## 2. Add a real WireGuard file and policy

Put a provider or private-network file at:

```text
~/.config/pr0xteus/secrets/wireguard/us-example.conf
```

The name used in the pool is the filename without `.conf`. Replace the pool
skeleton with:

```yaml
pools:
  us:
    region: north-america
    purpose: private-service-egress
    configs: [us-example]
    exit_countries:
      us-example: US
```

Set the requested-country policy in `config/egress-routing.yaml`:

```yaml
country_to_pool:
  US: us
default_pool: us
```

`exit_countries` is required for filenames that do not communicate their exit
country. It beats the old filename guess.

## 3. Start it

```bash
pr0xteus start
curl --fail --silent http://127.0.0.1:9091/healthz
```

`pr0xteus start` checks the token, bundle, pools, and routing before it pulls
images or starts anything. `pr0xteus status`, `pr0xteus logs --follow`,
`pr0xteus stop`, and `pr0xteus restart` are the normal day-to-day commands.

The controller API is `127.0.0.1:8000`; metrics and health are
`127.0.0.1:9091`. Cells have no host port. They join the private egress network
and the controller-only cell-control network; callers use the controller's
SOCKS gateway instead of joining either one. `/healthz` says the controller is
alive; it does not prove that a provider tunnel can be allocated right now.

### Optional: put the API on your tailnet

To route only through the sidecar, remove all pr0xteus host-port bindings. Add
this to `~/.config/pr0xteus/.env` and start again:

```dotenv
PR0XTEUS_TAILSCALE_ENABLED=true
PR0XTEUS_DISABLE_HOST_PORTS=true
TS_AUTHKEY=your-tailscale-auth-key
TS_HOSTNAME=pr0xteus
```

```bash
pr0xteus start
```

The optional sidecar gets its own tailnet identity and serves the controller
API on port 80, private metrics on port 9091, and the controller SOCKS gateway
on port 1080 through Tailscale Serve. This makes the hop entirely internal to
Docker: the controller, metrics, and SOCKS ports are not bound on the host. The bearer token below is still
required. Its identity survives restarts in `~/.config/pr0xteus/tailscale/state`.
Use the tailnet URL for API calls in this mode; the wrapper also returns a
SOCKS URL using that MagicDNS host. The loopback examples below apply to the
default `PR0XTEUS_DISABLE_HOST_PORTS=false` deployment.
Keep tailnet access to port 9091 restricted because its health and Prometheus
endpoints do not require authentication.

## 4. Allocate one configured exit

Read the token from `.env` without putting it in command history. The returned
proxy URL is a short-lived credential, so keep it out of logs and do not share
it:

```bash
token="$(sed -n 's/^PR0XTEUS_API_TOKEN=//p' ~/.config/pr0xteus/.env)"
auth_header=(--header @<(printf 'Authorization: Bearer %s' "$token"))

allocation="$(
  curl --fail-with-body --request POST \
    "${auth_header[@]}" \
    --header 'Content-Type: application/json' \
    --data '{"country":"US"}' \
    http://127.0.0.1:8000/v1/proxies
)"
proxy_url="$(jq -er '.url' <<<"$allocation")"
jq '{pool, exitCountry, url}' <<<"$allocation"
```

The response has this shape:

```json
{
  "url": "socks5://lease-id:lease-secret@127.0.0.1:1080",
  "pool": "us",
  "exitCountry": "US",
  "expiresAt": "2026-01-01T00:15:00Z"
}
```

That URL targets the controller SOCKS gateway. In the default deployment it
uses the loopback listener, so it is usable from the host shell or another
trusted client that can reach that listener. With
`PR0XTEUS_DISABLE_HOST_PORTS=true` and the wrapper-managed Tailscale profile,
the returned URL instead uses the sidecar's MagicDNS host on port 1080 and is
usable from another tailnet machine. In both cases the controller validates the
lease credentials, forwards bytes to the selected cell over the internal
control network, and the cell resolves and egresses through WireGuard.

## 5. Prove traffic uses the WireGuard exit

```bash
curl --fail --silent --show-error \
  --proxy "$proxy_url" https://api.ipify.org

unset token proxy_url allocation
unset -a auth_header
```

That prints the public address seen through the allocated exit. Use only
providers and targets you are authorized to use; the IP endpoint is a smoke
check, not a pr0xteus dependency.

## Source development

Cloning the repository is only for changing Pr0xteus itself. The Makefile puts
formatting, linting, unit tests, Testcontainers integration tests, and local
image builds inside the development container:

```bash
git clone https://github.com/psyb0t/pr0xteus.git
cd pr0xteus
make test
make lint
```

`make config-init` and `make run` exercise the production lifecycle with local
images. They run the actual installer in development mode, which writes ignored
`.config/pr0xteus/` and installs the wrapper there. Make invokes that installed
wrapper with the local config directory. The lifecycle and `.env` switches are
the same as an installed stack; only its `:dev` image pin and pull policy differ.
Production operators use the installer and `pr0xteus` command above.
