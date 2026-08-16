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

**Download the installer and read it before running it — never pipe `curl`
straight into a shell.** Confirm it only fetches the pinned image, runs the
image's `config init`, and installs the `pr0xteus` command — then run it.

```bash
# 1. Download (do not pipe curl into a shell).
curl -fsSL https://raw.githubusercontent.com/psyb0t/pr0xteus/main/install.sh -o pr0xteus-install.sh

# 2. Inspect — read the whole thing.
less pr0xteus-install.sh

# 3a. Per-user install (no root): command -> ~/.local/bin, config ->
#     ~/.config/pr0xteus, just for the current user.
bash pr0xteus-install.sh

# 3b. Or system-wide: command -> /usr/local/bin, config -> /etc/pr0xteus
#     (root-owned, readable by the `docker` group so any docker-group operator
#     drives the one shared stack).
sudo bash pr0xteus-install.sh --system
```

The mode auto-detects from who runs it (root → system-wide, otherwise
per-user); force it with `--user` or `--system`. Append `--rolling` to pin the
moving `:latest` instead of the latest release. A per-user install that finds
`~/.local/bin` off `PATH` prints the exact bash/zsh one-liner to add it.

The installer creates ignored local files only when absent (per-user paths
shown; a system-wide install uses `/etc/pr0xteus` instead of `~/.config/pr0xteus`):

```text
~/.config/pr0xteus/secrets/wireguard/*.conf      real provider or private-network files
~/.config/pr0xteus/secrets/pools.yaml            approved logical pools
~/.config/pr0xteus/config/egress-routing.yaml    country -> pool policy
~/.config/pr0xteus/.env                           bearer token, host path, image and ports
```

The bearer token is `PR0XTEUS_API_TOKEN` in owner-only `.env`, not a separate
secret file. The installer owns the absolute host path the controller needs
when it asks Docker to bind one chosen file into a cell.

Put an authorized `*.conf` file in `~/.config/pr0xteus/secrets/wireguard/`, then make
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
(prompts before deleting `~/.config/pr0xteus`). Append `--rolling` to `start`/`upgrade`
to use the moving `:latest` image for one run.

## Run it with Docker directly

The `pr0xteus` command is only a guardrail around Docker: it pulls the pinned
image, runs the image's `config init`, and drives `docker compose`. To do it
yourself against a config directory you own — no installer, no wrapper — pin a
released tag (not `:latest`) and reproduce those steps:

```bash
config_dir=~/.config/pr0xteus            # any directory you own
image=psyb0t/pr0xteus:vX.Y.Z             # pin a released tag
mkdir -p "$config_dir"
docker pull "$image"

# Scaffold compose + .env + config skeleton (preserves existing files).
docker run --rm --user "$(id -u):$(id -g)" \
  -v "$config_dir:/config" \
  "$image" config init \
  --config-dir /config \
  --host-config-dir "$config_dir" \
  --controller-image "$image"

# Fill secrets/wireguard/*.conf, secrets/pools.yaml, config/egress-routing.yaml,
# and PR0XTEUS_API_TOKEN in .env (see above), then bring the stack up:
docker compose --project-directory "$config_dir" \
  --env-file "$config_dir/.env" \
  -f "$config_dir/docker-compose.yml" up -d
```

`--host-config-dir` must be the real host path so the controller can bind one
chosen WireGuard file into a cell. This is exactly what `pr0xteus setup` +
`pr0xteus start` do for you.

## Allocate and prove a proxy

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
