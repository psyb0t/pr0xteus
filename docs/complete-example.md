# Complete image-first setup and a real SOCKS5 proof

This is the operator path: pull the published image, generate only ignored
local configuration, start it with `docker compose`, then allocate one private
WireGuard-backed SOCKS5 exit. You need Linux, Docker with the built-in `docker
compose` command, and WireGuard material you are authorized to use. pr0xteus
does not create a VPN account or fetch provider configuration for you.

For component detail, see [architecture.md](architecture.md),
[api.md](api.md), [internal/README.md](../internal/README.md), and
[cell/README.md](../cell/README.md).

## 1. Create the local deployment directory

```bash
mkdir pr0xteus && cd pr0xteus
curl -fsSLO https://raw.githubusercontent.com/psyb0t/pr0xteus/main/docker-compose.yml
docker run --rm --user "$(id -u):$(id -g)" -v "$PWD:/config" \
  psyb0t/pr0xteus:latest config init \
  --config-dir /config --host-config-dir "$PWD"
```

The initializer is non-destructive. It creates these local, ignored files only
when missing:

```text
secrets/wireguard/           put real *.conf files here
secrets/pools.yaml           approved logical pools
config/egress-routing.yaml   requested country -> pool mapping
.env                         bearer token, host path, image and ports
```

`.env` is mode `0600`; it contains `PR0XTEUS_API_TOKEN`. Keep it local. There
is no separate Compose `secrets/` token file.

To deploy a fixed controller release, replace `latest` consistently:

```bash
docker run --rm --user "$(id -u):$(id -g)" -v "$PWD:/config" \
  psyb0t/pr0xteus:vX.Y.Z config init \
  --config-dir /config --host-config-dir "$PWD" \
  --controller-image psyb0t/pr0xteus:vX.Y.Z
```

The controller carries its matching cell reference: `latest` carries
`cell-latest`; `vX.Y.Z` carries `cell-vX.Y.Z`. Do not set a cell override
unless you need one; if you do, it must include an immutable digest.

## 2. Add a real WireGuard file and policy

Put a provider or private-network file at:

```text
secrets/wireguard/us-example.conf
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

## 3. Validate, pull, and start

```bash
docker run --rm --user "$(id -u):$(id -g)" -v "$PWD:/config:ro" \
  psyb0t/pr0xteus:latest config check --config-dir /config
docker compose pull
docker compose up -d
curl --fail --silent http://127.0.0.1:9091/healthz
```

The controller API is `127.0.0.1:8000`; metrics and health are
`127.0.0.1:9091`. Cells have no host port and live only on
`pr0xteus-egress`. `/healthz` says the controller is alive; it does not prove
that a provider tunnel can be allocated right now.

## 4. Allocate one configured exit

Read the token from `.env` without putting it in command history or curl's
argument list:

```bash
token="$(sed -n 's/^PR0XTEUS_API_TOKEN=//p' .env)"
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
the image-first commands above.
