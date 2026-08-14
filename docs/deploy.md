# Deployment guide

This guide assumes a Linux host with Docker Engine and Docker Compose v2. The
standard deployment is private-by-default: the API and metrics ports bind only
to loopback, and the WireGuard material stays outside Git and image layers.

If you want the exact empty-checkout-to-egress route, start with
[complete-example.md](complete-example.md). This page is the operational
runbook around that setup, not a second competing quick start.

## Prepare local configuration

Run the first setup target from the repository root:

```bash
make config-init
```

It preserves existing local files. On a new checkout it creates ignored copies
of `examples/pools.yaml` and `examples/egress-routing.yaml`, an ignored API
token file, and an ignored `.env`. Put your provider's real `*.conf` files in
`secrets/wireguard/`, then replace the example pool entry with their basenames.
Do not commit any of those files.

The pool config name is the WireGuard filename without `.conf`. If a provider
filename does not reveal its exit country, put that country in the pool's
`exit_countries` map. The complete example shows the small working shape.

The controller bind-mounts the bundle at the same absolute path inside itself
because Docker performs the per-cell mount on behalf of the controller. The
path in `.env` must therefore be an absolute host path.

## Choose cell image mode

For local development, build both images and keep the generated local tag:

```bash
make build-cell
make config-check
make run
```

For a published deployment, the controller image already selects its matching
cell tag: `latest` selects `cell-latest`, while `vX.Y.Z` selects
`cell-vX.Y.Z`. To deliberately override that pairing, set
`PR0XTEUS_CELL_IMAGE` in `.env` to a released cell image **with its SHA-256
digest** and leave `PR0XTEUS_ALLOW_UNPINNED_CELL_IMAGE=false`. The service
rejects mutable operator overrides by default.

## Verify without leaking the token

Use a secret-aware client in normal operations. For a local manual check, read
the token into a shell variable and pass the header by file descriptor, so the
secret is neither in shell history nor in curl's argument list:

```bash
token="$(<secrets/pr0xteus_api_token)"
curl --fail-with-body \
  --header @<(printf 'Authorization: Bearer %s' "$token") \
  --header 'Content-Type: application/json' \
  --data '{"country":"US"}' \
  http://127.0.0.1:8000/v1/proxies
```

The response has a private `socks5://...` URL. Use it from a container joined
to `pr0xteus-egress`, or adapt the standalone host-loopback smoke-test mode;
do not expose a cell's port to a public interface.

## Runtime limits and exceptions

The controller and socket proxy are non-root, read-only, capability-empty,
resource-capped, and use bounded Docker JSON logs. Cells are the deliberately
small exception: creating a WireGuard interface and firewall requires
`NET_ADMIN`, `/dev/net/tun`, and temporary root during startup. `SETUID` and
`SETGID` exist only so `su-exec` can make its final one-way drop to UID 1500;
the entrypoint then starts microSocks as that non-root user. Its filesystem
remains writable only for
ephemeral `wg0` and resolver setup; it receives one read-only config file and
does not receive Docker credentials.

The cell kill switch begins with default-drop firewall rules. Before the tunnel
is ready it permits only the resolved UDP WireGuard peer; after the handshake
it permits tunnel traffic and the private SOCKS5 listener. A failed handshake
causes startup to fail rather than handing out a proxy that may leak traffic.

## Operations

```bash
make config-check  # validate Compose interpolation
make audit         # Go vulnerability scan
make audit-compose # catch unsafe Compose regressions
make build         # rebuild the orchestrator image
make run           # rebuild both images and start the stack
```

`make run` is intentionally a deployment action; it starts persistent
containers. `make build`, `make build-cell`, linting, and test targets do not.

For configuration or provider changes, rebuild as needed and use `make run`.
Do not use a bare Compose restart after source changes: it retains the old
image and therefore the old binary.

## Integration verification

`make test-integration` is a self-contained Testcontainers check. It builds
the production controller and cell images, then starts a real WireGuard peer,
the controller, the spawned cell, and a SOCKS5 client on an isolated Docker
network. The test requests a proxy through the real API and verifies a request
reaches the peer's private HTTP endpoint through that proxy.

It creates fresh test-only keys and configuration, needs no provider account or
production bundle, maps no host ports, and removes only its own Testcontainers
resources when it exits.

`make test-coverage` executes every unit and Testcontainers package with
`go test -tags=integration -race ./...` and requires at least 90% coverage of
pr0xteus production code (`internal/pkg/services/pr0xteus` and `pkg/client`).
The test source itself runs but is not part of Go's production-code coverage
denominator. The test-only controller image writes native coverage into
ignored `.cover/` storage, which is merged with the Go test profile; normal
integration runs continue to use the production controller image.

For an explicit provider-account smoke test, run `make test-real` (or set a
country selector with `PR0XTEUS_REAL_TEST_COUNTRY=US make test-real`). It is
not part of CI or the normal test suite: it uses ignored local Surfshark
material at `secrets/wg/surfshark-wireguard/`, `secrets/wg/pools.yaml`, and
`config/egress-routing.yaml`. The test creates a separate controller, network,
cell, and consumer through Testcontainers, then checks that its SOCKS5 request
has a different public IPv4 address from its direct request. It uses a fresh
test token and does not touch persistent Compose services.
