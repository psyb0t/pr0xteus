# Control API

Prefix: API. Routes are registered in `APIServer.Register` (`api.go`). The public contract is documented in `docs/api.md`.

- **API1:** Routes live under `/v1`. Changing a route, field, status or default updates `docs/api.md`, the skill (`.agents/skills/pr0xteus/`) and `pkg/client` when it reads the field, in the same change.
- **API2:** JSON fields are camelCase (`exitCountry`, `fallbackOk`, `containerId`). The operator YAML files (`pools.yaml`, `egress-routing.yaml`) stay snake_case.
- **API3:** `POST /v1/proxies` takes a strict body: `Content-Type: application/json`, at most 16 KiB (`maxRequestBody`), unknown fields rejected, one JSON value, exactly one of `country` or `pool`.
- **API4:** Every collection (`/v1/proxies`, `/v1/pools`, `/v1/cells`) uses `collectionPage` and `pageItems`: `limit` default 100 and maximum 1000, `offset` default 0, and a response with `limit`, `offset` and `total`. A new collection does the same.
- **API5:** Errors use the aichteeteapee `ErrorResponse` envelope through `writeError`. The client sees a fixed code and message, never Docker or WireGuard error text. Log the full error server side.
- **API6:** A Docker discovery failure is a `500`, never an empty list or a `404`.
- **API7:** GET routes never allocate, spawn or issue credentials. Allocation happens only in `POST /v1/proxies`.
