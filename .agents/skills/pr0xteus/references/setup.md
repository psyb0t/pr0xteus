# pr0xteus setup

This is private egress plumbing. It hands a trusted container a SOCKS5 URL
after the controller has started a WireGuard-backed cell and seen a handshake.
It is not an internet-facing proxy. Keep the controller on loopback or an
authenticated private network, and use WireGuard material you are allowed to
use.

For the long-form operator walkthrough, see
[docs/complete-example.md](../../../../docs/complete-example.md). This
reference is the fast path an agent should follow without inventing paths,
tokens, or Docker flags.

## What is where

```text
secrets/wireguard/*.conf      real provider or private-network WireGuard files
secrets/pools.yaml            approved logical pools, ignored
config/egress-routing.yaml    country -> pool policy, ignored
secrets/pr0xteus_api_token    controller bearer token, ignored
.env                          absolute host paths and image choice, ignored
```

The controller must see the WireGuard bundle at its **same absolute host
path**. It asks Docker to mount one selected file into a cell; a made-up
container-only path will not work.

## First local setup

```bash
git clone https://github.com/psyb0t/pr0xteus.git
cd pr0xteus
make config-init
```

`make config-init` creates ignored skeletons and a local token on a fresh
checkout. It intentionally does **not** give you a usable VPN config. Put
real `*.conf` files in `secrets/wireguard/`, then make the policy match their
basenames.

Example for a file named `secrets/wireguard/us-example.conf`:

```yaml
# secrets/pools.yaml
pools:
  us:
    region: north-america
    purpose: private-service-egress
    configs: [us-example]
    exit_countries:
      us-example: US
```

```yaml
# config/egress-routing.yaml
country_to_pool:
  US: us
default_pool: us
```

For local builds, the generated `.env` already points at
`psyb0t/pr0xteus-cell:dev` with the local-only unpinned escape hatch enabled.
For a deployed stack, change it to a published digest and set:

```dotenv
PR0XTEUS_CELL_IMAGE=psyb0t/pr0xteus-cell@sha256:REPLACE_WITH_PUBLISHED_DIGEST
PR0XTEUS_ALLOW_UNPINNED_CELL_IMAGE=false
```

Build and validate before starting the persistent services:

```bash
make build-cell
make config-check
make run
curl --fail --silent http://127.0.0.1:9091/healthz
```

`make run` creates persistent Compose services. Do not run it just to inspect
the source or test a client.

## Allocate and prove a proxy

Read the local token without putting it in command history or a process argv:

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
printf 'allocated %s from pool %s\n' \
  "$(jq -r '.exitCountry' <<<"$allocation")" \
  "$(jq -r '.pool' <<<"$allocation")"
```

The URL is not reachable from your host. Join an intentionally short-lived
consumer to `pr0xteus-egress` and send an outbound request through SOCKS5:

```bash
docker run --rm --network pr0xteus-egress \
  curlimages/curl@sha256:d94d07ba9e7d6de898b6d96c1a072f6f8266c687af78a74f380087a0addf5d17 \
  --fail --silent --show-error \
  --proxy "$proxy_url" \
  https://api.ipify.org

unset token proxy_url allocation
unset -a auth_header
```

Use a destination that the operator permits. `api.ipify.org` is only an
egress smoke check; it is not part of the controller and should not be treated
as a guaranteed service dependency.

## Day-to-day control API

```bash
export PR0XTEUS_URL=http://127.0.0.1:8000
export PR0XTEUS_API_TOKEN=read-it-from-your-secret-store
auth_header=(--header @<(printf 'Authorization: Bearer %s' "$PR0XTEUS_API_TOKEN"))

# Current pool/tunnel view.
curl --fail-with-body "${auth_header[@]}" "$PR0XTEUS_URL/v1/pools" | jq .

# Ask for one named pool rather than country routing.
curl --fail-with-body "${auth_header[@]}" \
  --header 'Content-Type: application/json' \
  --data '{"pool":"us"}' \
  "$PR0XTEUS_URL/v1/proxies"
```

For a broken allocation, request a replacement with `excludeProxy`. Do not
look for a release endpoint: assignment tracking is not a proxy-session lease.

```bash
curl --fail-with-body "${auth_header[@]}" \
  --header 'Content-Type: application/json' \
  --data '{"country":"US","excludeProxy":"socks5://old-private-cell:1080"}' \
  "$PR0XTEUS_URL/v1/proxies"
```

## Commands that mean different things

| Command | What it does |
|---|---|
| make config-init | Creates missing ignored local skeletons; preserves existing config. |
| make config-check | Validates Compose interpolation and local config shape. |
| make build / make build-cell | Builds the controller / WireGuard-cell image. |
| make run | Rebuilds and starts the persistent private Compose stack. |
| make test | Runs unit tests and an isolated Testcontainers WireGuard/SOCKS5 stack. |
| make test-coverage | Runs every test package and requires 90% production-code coverage. |
| make test-real | Opt-in Surfshark smoke using ignored local test material; never CI. |
| make audit-compose | Checks Compose hardening assumptions. |

Everything test and lint related runs in the repository dev container. The
real test uses only the test-specific ignored Surfshark paths and fresh
Testcontainers resources; it does not touch a running Compose deployment.

## Things that usually bite

- config-init twice is harmless but pointless. The first invocation creates
  the skeleton; edit it and run make config-check.
- A config name is the filename without .conf. us-example.conf becomes
  us-example in configs and exit_countries.
- A public SOCKS URL is a bad architecture. The controller returns a
  private-network hostname on purpose.
- GET /healthz says the metrics listener is alive. It does not prove a
  provider tunnel can currently be allocated.
- If the controller says a selected config is unavailable, inspect its
  structured logs and pool view; do not weaken the cell firewall or expose the
  Docker socket to debug it.
