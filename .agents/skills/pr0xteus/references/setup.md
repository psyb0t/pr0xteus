# pr0xteus setup

Pr0xteus is private egress plumbing. A trusted container receives a SOCKS5 URL
only after the controller has started a WireGuard-backed cell and seen a
handshake. It is not an internet-facing proxy. Keep the controller on loopback
or an authenticated private network, and use WireGuard material you are
allowed to use.

For the full operator walkthrough, see
[docs/complete-example.md](../../../../docs/complete-example.md). This page is
the agent fast path: use the published image and its installer; do not invent
paths, tokens, or Docker flags.

## Operator setup

```bash
curl -fsSL https://raw.githubusercontent.com/psyb0t/pr0xteus/main/install.sh | sudo bash
```

The installer creates ignored local files only when absent:

```text
~/.pr0xteus/secrets/wireguard/*.conf      real provider or private-network files
~/.pr0xteus/secrets/pools.yaml            approved logical pools
~/.pr0xteus/config/egress-routing.yaml    country -> pool policy
~/.pr0xteus/.env                           bearer token, host path, image and ports
```

The bearer token is `PR0XTEUS_API_TOKEN` in owner-only `.env`, not a separate
secret file. The installer owns the absolute host path the controller needs
when it asks Docker to bind one chosen file into a cell.

Put an authorized `*.conf` file in `~/.pr0xteus/secrets/wireguard/`, then make
the policy match its basename. A file named `us-example.conf` uses `us-example`
below:

```yaml
pools:
  us:
    region: north-america
    purpose: private-service-egress
    configs: [us-example]
    exit_countries:
      us-example: US
```

```yaml
country_to_pool:
  US: us
default_pool: us
```

Start the image-first deployment:

```bash
pr0xteus start
curl --fail --silent http://127.0.0.1:9091/healthz
```

`pr0xteus start` checks the local token, WireGuard bundle, pools, and routing
before it starts containers. `latest` carries `cell-latest`; a versioned controller carries its matching
versioned cell. Do not set `PR0XTEUS_CELL_IMAGE` unless an operator explicitly
needs an override; that override must contain an immutable digest.

The installer pins to the latest tagged release, not `:latest`. Lifecycle
commands: `pr0xteus stop`, `pr0xteus status`, `pr0xteus logs`, `pr0xteus upgrade`
(re-pin to the newest release and drop the old image), and `pr0xteus uninstall`
(prompts before deleting `~/.pr0xteus`). Append `--rolling` to `start`/`upgrade`
to use the moving `:latest` image for one run.

## Allocate and prove a proxy

```bash
token="$(sed -n 's/^PR0XTEUS_API_TOKEN=//p' ~/.pr0xteus/.env)"
auth_header=(--header @<(printf 'Authorization: Bearer %s' "$token"))

allocation="$(
  curl --fail-with-body \
    "${auth_header[@]}" \
    --header 'Content-Type: application/json' \
    --data '{"country":"US"}' \
    http://127.0.0.1:8000/v1/proxies
)"
proxy_url="$(jq -er '.url' <<<"$allocation")"
```

The returned URL is private Docker-network plumbing. Prove it from a
short-lived container on `pr0xteus-egress`:

```bash
docker run --rm --network pr0xteus-egress \
  curlimages/curl@sha256:d94d07ba9e7d6de898b6d96c1a072f6f8266c687af78a74f380087a0addf5d17 \
  --fail --silent --show-error \
  --proxy "$proxy_url" \
  https://api.ipify.org

unset token proxy_url allocation
unset -a auth_header
```

## Agent API use

Use the private controller URL and bearer token supplied through the plugin's
sensitive configuration. Do not read the operator's `.env`, WireGuard files,
or Docker socket.

```bash
export PR0XTEUS_URL=http://127.0.0.1:8000
export PR0XTEUS_API_TOKEN=read-it-from-your-secret-store
auth_header=(--header @<(printf 'Authorization: Bearer %s' "$PR0XTEUS_API_TOKEN"))

curl --fail-with-body "${auth_header[@]}" "$PR0XTEUS_URL/v1/pools" | jq .
curl --fail-with-body "${auth_header[@]}" \
  --header 'Content-Type: application/json' \
  --data '{"pool":"us"}' \
  "$PR0XTEUS_URL/v1/proxies"
```

For a broken allocation, ask for a replacement with `excludeProxy`. Do not
look for a release endpoint: assignment tracking is not a proxy-session lease.
