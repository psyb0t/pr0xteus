# Complete image-first setup and a real SOCKS5 proof

This is the operator path: install the published image, add your own authorized
WireGuard configuration, start the private stack, then allocate one SOCKS5
exit. You need Linux and Docker; `docker compose` is already part of Docker.
pr0xteus does not create a VPN account or fetch provider configuration for you.

For component detail, see [architecture.md](architecture.md),
[api.md](api.md), [internal/README.md](../internal/README.md), and
[cell/README.md](../cell/README.md).

## 1. Install it

```bash
curl -fsSL https://raw.githubusercontent.com/psyb0t/pr0xteus/main/install.sh | sudo bash
```

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
run `pr0xteus setup`. Do not set a cell override unless you need one; if you
do, it must include an immutable digest.

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
images or starts anything. `pr0xteus status`, `pr0xteus logs --follow`, and
`pr0xteus stop` are the normal day-to-day commands.

The controller API is `127.0.0.1:8000`; metrics and health are
`127.0.0.1:9091`. Cells have no host port and live only on
`pr0xteus-egress`. `/healthz` says the controller is alive; it does not prove
that a provider tunnel can be allocated right now.

### Optional: put the API on your tailnet

Keep the loopback listener, then add this to `~/.config/pr0xteus/.env` and start
again:

```dotenv
PR0XTEUS_TAILSCALE_ENABLED=true
TS_AUTHKEY=your-tailscale-auth-key
TS_HOSTNAME=pr0xteus
```

```bash
pr0xteus start
```

The optional sidecar gets its own tailnet identity and proxies
`http://pr0xteus/v1/...` to the controller through Tailscale Serve. It opens
no host port; the bearer token below is still required. Its identity survives
restarts in `~/.config/pr0xteus/tailscale/state`.

## 4. Allocate one configured exit

Read the token from `.env` without putting it in command history or curl's
argument list:

```bash
token="$(sed -n 's/^PR0XTEUS_API_TOKEN=//p' ~/.config/pr0xteus/.env)"
auth_header=(--header @<(printf 'Authorization: Bearer %s' "$token"))

allocation="$(
  curl --fail-with-body \
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
  "url": "socks5://pr0xteus-tunnel-us-123:1080",
  "pool": "us",
  "exitCountry": "US"
}
```

That URL is Docker-network plumbing and is meant to fail from the host shell.

## 5. Prove traffic uses the private SOCKS5 cell

```bash
docker run --rm --network pr0xteus-egress \
  curlimages/curl@sha256:d94d07ba9e7d6de898b6d96c1a072f6f8266c687af78a74f380087a0addf5d17 \
  --fail --silent --show-error \
  --proxy "$proxy_url" \
  https://api.ipify.org

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

`make config-init` and `make run` are development conveniences. They use
`docker-compose.yml` plus `docker-compose.dev.yml`; production operators use
the installer and `pr0xteus` command above.
