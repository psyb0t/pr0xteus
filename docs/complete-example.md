# Complete local setup and a real SOCKS5 proof

This is the no-mystery route from a fresh clone to a working **private**
WireGuard-backed SOCKS5 cell. You need Linux, Docker Engine, Docker Compose
v2, Make, and a WireGuard configuration you are authorized to use. pr0xteus
does not create a VPN account or fetch provider configs for you.

The controller is local policy plus cell lifecycle. Your service requests a
country or a named pool; it never gets to choose an image, a Docker mount, or
a WireGuard file.

For why the pieces are separated this way, see
[architecture.md](architecture.md). For the HTTP contract, see
[api.md](api.md). The code-level controller and cell detail live in
[internal/README.md](../internal/README.md) and
[cell/README.md](../cell/README.md).

## 1. Create only local, ignored config

```bash
git clone https://github.com/psyb0t/pr0xteus.git
cd pr0xteus
make config-init
```

On a fresh checkout that creates these ignored local files:

```text
secrets/wireguard/           your real *.conf files go here
secrets/pools.yaml           the approved logical pools
config/egress-routing.yaml   requested country -> pool mapping
secrets/pr0xteus_api_token   generated bearer token
.env                         absolute host paths plus image choice
```

It does not create a usable VPN config. Put a real config file in the bundle.
For this example, use:

```text
secrets/wireguard/us-example.conf
```

The pool config name is the filename without `.conf`: `us-example`.

## 2. Make the local policy match the file

Replace the skeleton in `secrets/pools.yaml` with:

```yaml
pools:
  us:
    region: north-america
    purpose: private-service-egress
    configs: [us-example]
    exit_countries:
      us-example: US
```

Then set the country route in `config/egress-routing.yaml`:

```yaml
country_to_pool:
  US: us
default_pool: us
```

`exit_countries` matters when a provider filename does not say what country it
exits from. It wins over the old `<country>-<location>.conf` filename guess.

## 3. Check the host path and image

`make config-init` writes an `.env` for local development:

```dotenv
PR0XTEUS_CELL_IMAGE=psyb0t/pr0xteus:cell-dev
PR0XTEUS_ALLOW_UNPINNED_CELL_IMAGE=true
PR0XTEUS_BUNDLE_DIR=/absolute/path/to/pr0xteus/secrets/wireguard
PR0XTEUS_POOLS_FILE=/absolute/path/to/pr0xteus/secrets/pools.yaml
PR0XTEUS_ROUTING_FILE=/absolute/path/to/pr0xteus/config/egress-routing.yaml
PR0XTEUS_API_TOKEN_FILE=/absolute/path/to/pr0xteus/secrets/pr0xteus_api_token
```

If an older local `.env` says `psyb0t/pr0xteus-cell:dev`, change that image
value to `psyb0t/pr0xteus:cell-dev`. `make config-init` preserves an existing
file and only warns about the stale value.

Those paths are deliberately absolute. The controller asks Docker to mount the
selected WireGuard file into a cell, so it must see the bundle at the same
absolute host path Docker sees. A container-only path will not work.

For a deployed stack, omit `PR0XTEUS_CELL_IMAGE` to use the matching cell
already baked into the controller release (`latest` -> `cell-latest`; `vX.Y.Z`
-> `cell-vX.Y.Z`). To deliberately override that pairing, use a released tag
with its digest and disable the local escape hatch:

```dotenv
PR0XTEUS_CELL_IMAGE=psyb0t/pr0xteus:cell-vX.Y.Z@sha256:REPLACE_WITH_PUBLISHED_DIGEST
PR0XTEUS_ALLOW_UNPINNED_CELL_IMAGE=false
```

Do not put the token, any WireGuard file, or either version of `.env` in Git.

## 4. Validate and start

```bash
make build-cell
make config-check
make run
curl --fail --silent http://127.0.0.1:9091/healthz
```

`make run` rebuilds and starts the persistent Compose stack. It exposes:

| Listener | Default | Who can use it |
|---|---|---|
| Control API | `127.0.0.1:8000` | Bearer-token holders |
| Health and metrics | `127.0.0.1:9091` | Local monitoring and ops |
| SOCKS5 cells | no host port | Containers on `pr0xteus-egress` |

`/healthz` proves that the metrics listener is up. It does not promise the
provider is reachable or that a cell can be allocated right now.

## 5. Allocate one configured exit

Read the token without putting it in shell history or a process argument:

```bash
token="$(<secrets/pr0xteus_api_token)"
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

The hostname is private Docker-network plumbing. It is supposed to fail from
your host shell.

## 6. Prove traffic uses the private SOCKS5 cell

Run a disposable consumer on the egress network:

```bash
docker run --rm --network pr0xteus-egress \
  curlimages/curl@sha256:d94d07ba9e7d6de898b6d96c1a072f6f8266c687af78a74f380087a0addf5d17 \
  --fail --silent --show-error \
  --proxy "$proxy_url" \
  https://api.ipify.org

unset token proxy_url allocation
unset -a auth_header
```

That prints the public address seen through the allocated exit. Use the
provider and target you are authorized to use; the IP-echo endpoint is a
simple smoke check, not a pr0xteus dependency.

## 7. Inspect and replace assignments

```bash
export PR0XTEUS_URL=http://127.0.0.1:8000
export PR0XTEUS_API_TOKEN=read-it-from-your-secret-store
auth_header=(--header @<(printf 'Authorization: Bearer %s' "$PR0XTEUS_API_TOKEN"))

# Pools, hot tunnel state, and recent failures.
curl --fail-with-body "${auth_header[@]}" "$PR0XTEUS_URL/v1/pools" | jq .

# A caller that had a broken proxy asks for a different one.
curl --fail-with-body "${auth_header[@]}" \
  --header 'Content-Type: application/json' \
  --data '{"country":"US","excludeProxy":"socks5://old-private-cell:1080"}' \
  "$PR0XTEUS_URL/v1/proxies"
```

There is no explicit release endpoint. The controller tracks the API
assignment, not the lifetime of every SOCKS5 session. A healthy cell stays warm
until the idle reaper decides it is done.

## What to run when

| Need | Command |
|---|---|
| Make missing local skeletons | `make config-init` |
| Validate Compose interpolation | `make config-check` |
| Start persistent local services | `make run` |
| Run unit plus real isolated WireGuard/SOCKS5 tests | `make test` |
| Run the ignored local Surfshark smoke | `make test-real` |
| Run every test package and enforce 90% production coverage | `make test-coverage` |
| Check Go vulnerabilities / Compose hardening | `make audit` / `make audit-compose` |

All lint and test targets run their tooling inside the repository development
container. The normal integration suite creates its own Docker resources via
Testcontainers; it does not reuse or need this persistent Compose stack.
