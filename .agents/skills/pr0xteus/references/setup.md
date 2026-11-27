# pr0xteus setup

Pr0xteus is private egress plumbing. A trusted client receives SOCKS5 and HTTP
proxy URLs only after the controller has started a WireGuard-backed cell and
confirmed a handshake. It is not an internet-facing proxy. Keep the controller on loopback,
remove host bindings for an authenticated private-network gateway, or
deliberately configure another protected bind address. Use WireGuard material
you are allowed to use.

For the full operator walkthrough, see
[the complete example](https://github.com/psyb0t/pr0xteus/blob/main/docs/complete-example.md). This page is
the agent fast path: use the published image and its installer; do not invent
paths, tokens, or Docker flags.

## Operator setup

**Download the installer and read it before running it. Never pipe `curl`
straight into a shell.** Confirm it only fetches the pinned image, runs the
image's `config init`, and installs the `pr0xteus` command, then run it.

Run setup only when the user asked for it. The per-user install needs no root. The system-wide install uses `sudo` to write `/usr/local/bin` and `/etc/pr0xteus`, so offer it only when the user wants one shared stack.

```bash
# 1. Download (do not pipe curl into a shell).
curl -fsSL https://raw.githubusercontent.com/psyb0t/pr0xteus/main/install.sh -o pr0xteus-install.sh

# 2. Inspect: read the whole thing.
cat pr0xteus-install.sh

# 3a. Per-user install (no root): command -> ~/.local/bin, config ->
#     ~/.config/pr0xteus, just for the current user.
bash pr0xteus-install.sh

# 3b. Or system-wide: command -> /usr/local/bin, config -> /etc/pr0xteus
#     (root-owned, readable by the `docker` group so any docker-group operator
#     drives the one shared stack).
sudo bash pr0xteus-install.sh --system
```

The mode auto-detects from who runs it (root gets system-wide, otherwise
per-user); force it with `--user` or `--system`. Append `--rolling` to pin the
moving `:latest` instead of the latest release. A per-user install that finds
`~/.local/bin` off `PATH` prints the exact bash/zsh one-liner to add it.

The installer creates ignored local files only when absent (per-user paths
shown; a system-wide install uses `/etc/pr0xteus` instead of `~/.config/pr0xteus`):

```text
~/.config/pr0xteus/secrets/wireguard/*.conf      real provider or private-network files
~/.config/pr0xteus/secrets/pools.yaml            approved logical pools
~/.config/pr0xteus/config/egress-routing.yaml    country -> pool policy
~/.config/pr0xteus/.env                           settings: bearer token, host path, image and ports
~/.config/pr0xteus/.env.example                   refreshed reference; safe to inspect
```

The bearer token is `PR0XTEUS_API_TOKEN` in that owner-only settings file, not a separate secret file. The operator moves it into a secret store for you. The installer owns the absolute host path the controller needs
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

`pr0xteus start` checks the local token, WireGuard bundle, pools, and routing before it pulls images and starts containers. The stack mounts the host Docker socket into a socket-proxy container, and the controller reaches Docker only through that proxy. Each published controller pulls the cell image
built from its own commit (`cell-sha-<commit>`; releases up to v0.11.6 pull
`cell-vX.Y.Z`) when it first needs a cell; its image is not an
operator setting.

The installer pins to the latest tagged release, not `:latest`. Lifecycle
commands: `pr0xteus stop`, `pr0xteus restart`,
`pr0xteus status`, `pr0xteus logs`, `pr0xteus upgrade`
(refreshes `.env.example` and managed templates, re-pins to the newest
release, refreshes the command, starts through that command, and drops the old
image), and `pr0xteus uninstall` (prompts before deleting
`~/.config/pr0xteus`). Append `--rolling` to `start`/`upgrade` to use the moving
`:latest` image for one run.

To reach the controller from a tailnet without binding controller ports on the
host, set `PR0XTEUS_TAILSCALE_ENABLED=true` and
`PR0XTEUS_DISABLE_HOST_PORTS=true`; see the sidecar option in
[the deploy guide](https://github.com/psyb0t/pr0xteus/blob/main/docs/deploy.md#tailscale-sidecar).

## Run it with Docker directly

The `pr0xteus` command is only a guardrail around Docker: it pulls the pinned
image, runs the image's `config init`, and drives `docker compose`. To do it
yourself against a config directory you own (no installer, no wrapper), reproduce those steps. Run a released tag, not `:latest`: add `:vX.Y.Z` to `psyb0t/pr0xteus` in the three places below. The copy of this skill published to ClawHub already carries the release tag and digest.

```bash
config_dir=~/.config/pr0xteus            # any directory you own
mkdir -p "$config_dir"
docker pull psyb0t/pr0xteus

# Scaffold compose, settings and the config skeleton, and refresh the example
# settings file (an existing settings file stays untouched).
docker run --rm --user "$(id -u):$(id -g)" \
  -v "$config_dir:/config" \
  psyb0t/pr0xteus config init \
  --config-dir /config \
  --host-config-dir "$config_dir" \
  --controller-image psyb0t/pr0xteus

# Fill secrets/wireguard/*.conf, secrets/pools.yaml, config/egress-routing.yaml,
# and PR0XTEUS_API_TOKEN in the settings file (see above), then bring the stack up:
docker compose --project-directory "$config_dir" \
  --env-file "$config_dir/.env" \
  -f "$config_dir/docker-compose.yml" \
  -f "$config_dir/docker-compose.host-ports.yml" up -d
```

If the settings file sets `PR0XTEUS_DISABLE_HOST_PORTS=true`, add
`-f "$config_dir/docker-compose.no-host-ports.yml"` after the base Compose
file *instead of* `docker-compose.host-ports.yml`. The wrapper does this
automatically.

`--host-config-dir` must be the real host path so the controller can bind one
chosen WireGuard file into a cell. This is exactly what `pr0xteus setup` +
`pr0xteus start` do for you.

## Allocate and prove a proxy

Take the token from the environment or your secret store, as the agent API section below does. Do not read it out of the operator's configuration directory.

```bash
export PR0XTEUS_URL=http://127.0.0.1:8000
export PR0XTEUS_API_TOKEN=read-it-from-your-secret-store
auth_header=(--header @<(printf 'Authorization: Bearer %s' "$PR0XTEUS_API_TOKEN"))

allocation="$(
  curl --fail-with-body --request POST \
    "${auth_header[@]}" \
    --header 'Content-Type: application/json' \
    --data '{"country":"US"}' \
    "$PR0XTEUS_URL/v1/proxies"
)"
socks5_proxy="$(jq -er '.proxies.socks5' <<<"$allocation")"
http_proxy="$(jq -er '.proxies.http' <<<"$allocation")"
```

The returned URLs are short-lived credentials for the controller SOCKS5 gateway
and HTTP proxy. Use either directly from the host; the controller forwards them
to the selected cell over the internal network, and the cell owns WireGuard
egress. To inspect active
exits without making another allocation:

```bash
curl --fail-with-body "${auth_header[@]}" \
  "$PR0XTEUS_URL/v1/proxies?limit=100" | jq .
```

Use the allocated URL for real traffic. This proof asks `api.ipify.org`, a public service, for the exit IP, so run it only when the user wants that check:

```bash
curl --fail --silent --show-error \
  --proxy "$socks5_proxy" https://api.ipify.org

curl --fail --silent --show-error \
  --proxy "$http_proxy" https://api.ipify.org

unset PR0XTEUS_API_TOKEN socks5_proxy http_proxy allocation
unset -a auth_header
```

## Cells

The controller also exposes the live cell state behind the pools above:
observability plus on-demand teardown, discovered straight from Docker (the
`pr0xteus.parent.id` label), not from in-memory bookkeeping:

```bash
curl --fail-with-body "${auth_header[@]}" "$PR0XTEUS_URL/v1/cells" | jq .

# containerID is a "containerId" value from the list above.
curl --fail-with-body "${auth_header[@]}" "$PR0XTEUS_URL/v1/cells/$containerID" | jq .
```

Each cell view carries `containerId`, `parentId`, `pool`, `confName`, `state`
(Docker's own container state), `exitCountry`, `createdAt`, `uptimeSeconds`,
and a `traffic` snapshot (`requests`, `bytesUp`, `bytesDown`, `active`,
`dialFailures`, `destinations`) scraped from the cell's own `/status`.
`traffic` is omitted and `statusError` set when the controller can't reach a
cell's control server. `GET /v1/cells/{containerID}` 404s when the ID isn't
tracked.

`DELETE /v1/cells/{containerID}` stops that cell's container and clears its
pool slot so the next request re-spawns; `204` on success, `404` when
untracked. Only destroy a cell your own task allocated, and only when the
user asked for it.

```bash
curl --fail-with-body --request DELETE \
  "${auth_header[@]}" "$PR0XTEUS_URL/v1/cells/$containerID"
```

## Agent API use

Use the private controller URL and bearer token supplied through the plugin's
sensitive configuration. Do not read the operator's settings file, WireGuard
files, or Docker socket.

```bash
export PR0XTEUS_URL=http://127.0.0.1:8000
export PR0XTEUS_API_TOKEN=read-it-from-your-secret-store
auth_header=(--header @<(printf 'Authorization: Bearer %s' "$PR0XTEUS_API_TOKEN"))

curl --fail-with-body "${auth_header[@]}" "$PR0XTEUS_URL/v1/pools" | jq .
curl --fail-with-body --request POST "${auth_header[@]}" \
  --header 'Content-Type: application/json' \
  --data '{"pool":"us"}' \
  "$PR0XTEUS_URL/v1/proxies"
```

For a broken allocation, ask for a replacement with `excludeProxy`. Do not
look for a release endpoint: assignment tracking is not a proxy-session lease.
