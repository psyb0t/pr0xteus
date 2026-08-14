# Deployment guide

Pr0xteus is deployed from `psyb0t/pr0xteus`, not from a source checkout. You
need Linux, Docker, and its built-in `docker compose` command. The standard
stack keeps API and metrics loopback-only, confines raw Docker access to the
socket proxy, and keeps WireGuard material plus the bearer token outside Git
and image layers.

For the exact working configuration, use
[complete-example.md](complete-example.md). For controller/cell internals, see
[internal/README.md](../internal/README.md) and [cell/README.md](../cell/README.md).

## Bootstrap the host directory

```bash
mkdir pr0xteus && cd pr0xteus
curl -fsSLO https://raw.githubusercontent.com/psyb0t/pr0xteus/main/docker-compose.yml
docker run --rm --user "$(id -u):$(id -g)" -v "$PWD:/config" \
  psyb0t/pr0xteus:latest config init \
  --config-dir /config --host-config-dir "$PWD"
```

`config init` preserves existing configuration. It creates `.env`, the pools
and routing skeletons, and empty `secrets/wireguard/`; it never manufactures a
provider configuration. The random bearer token is written directly to the
ignored, owner-only `.env` as `PR0XTEUS_API_TOKEN`.

Add real `*.conf` files under `secrets/wireguard/`, then change
`secrets/pools.yaml` and `config/egress-routing.yaml` to reference their
basenames. The controller needs the same absolute host configuration path that
Docker sees because it asks Docker to bind a selected file into each cell.

## Pull and start a release

```bash
docker run --rm --user "$(id -u):$(id -g)" -v "$PWD:/config:ro" \
  psyb0t/pr0xteus:latest config check --config-dir /config
docker compose pull
docker compose up -d
```

`latest` uses its baked-in `cell-latest` image. A versioned controller uses its
matching versioned cell. If you bootstrap a versioned controller, pass that
same tag to both the `docker run` image and `--controller-image`; no cell
override is normally needed. A deliberate `PR0XTEUS_CELL_IMAGE` override must
be a released digest and leave `PR0XTEUS_ALLOW_UNPINNED_CELL_IMAGE=false`.

## Verify without leaking the bearer token

```bash
token="$(sed -n 's/^PR0XTEUS_API_TOKEN=//p' .env)"
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
docker compose ps
docker compose logs --tail=200 pr0xteus
docker compose pull
docker compose up -d
```

The controller and socket proxy use capped JSON logs. For configuration or
provider changes, run `config check` first, then use `docker compose up -d`.

Source checkout is development-only. Its Makefile runs format, lint, and all
tests in the development container; it is not required to operate the image.
