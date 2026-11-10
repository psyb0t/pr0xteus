# Internal control plane

This package is the pr0xteus controller: the bearer-authenticated control API, pool selection, lease credentials, the SOCKS5 and HTTP proxy gateways, the Docker cell spawner and the reaper. It is not a public Go API; external users talk to the HTTP API or import `pkg/client`.

The responsibility split, invariants, cell contract and hardening boundary are documented in [controller internals](https://psyb0t.github.io/pr0xteus/controller/).
