# Third-party notices

This repository's own source is MIT licensed; see [LICENSE](LICENSE). The
repository vendors the Go modules used to build the orchestrator, so their
upstream license and notice files remain in `vendor/` next to their source.
That directory is the authoritative complete notice set for a source release.

## Runtime binary

The production Go binary is built from the runtime imports declared in
`go.mod`. The notable direct dependencies are:

- `github.com/moby/moby/api` and `github.com/moby/moby/client` — Apache-2.0;
  the vendored client license is at `vendor/github.com/moby/moby/client/LICENSE`.
- `github.com/prometheus/client_golang` — Apache-2.0; notice at
  `vendor/github.com/prometheus/client_golang/LICENSE`.
- `golang.org/x/net` — BSD-style; notice at `vendor/golang.org/x/net/LICENSE`.
- `gopkg.in/yaml.v3` — Apache-2.0 plus MIT-origin files; notice at
  `vendor/gopkg.in/yaml.v3/LICENSE`.
- `github.com/psyb0t/aichteeteapee`, `ctxerrors`, and `gonfiguration` — MIT.
- `github.com/psyb0t/common-go` — WTFPL, as declared by its upstream
  repository. It is source-owned within the same `psyb0t` account.

## Cell image

The cell image builds [microSocks](https://github.com/rofl0r/microsocks) from
the exact `v1.0.5` commit verified in `cell/Dockerfile`. microSocks is MIT
licensed; its `COPYING` file names copyright © 2017 rofl0r. The image also
contains unmodified Alpine packages, including WireGuard tooling, iproute2,
iptables, Bash, and `su-exec`; their package licenses are supplied by Alpine.

## Development-only tools

`golangci-lint` is a GPL-3.0 developer tool listed in the Go `tool` block.
It is not imported by, linked into, or shipped in the production orchestrator
or cell image. Its vendored source and GPL notice remain at
`vendor/github.com/golangci/golangci-lint/v2/LICENSE` for source-distribution
completeness.

The Compose stack pulls `tecnativa/docker-socket-proxy` as a separate image;
it is not copied into this repository or linked into the Go binary. Consult
that image's upstream repository for its current license and notices.

This is a factual dependency inventory, not legal advice. Recheck licenses
when adding, removing, or changing dependencies or distribution models.
