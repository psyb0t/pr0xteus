---
title: "Go client"
description: "Use the pr0xteus pkg/client Go package: request proxy leases, send HTTP through them with retries, pick VPN-only or public-first mode, and run the preflight IP check."
nav_order: 5
---

# Go client

[`pkg/client`](https://github.com/psyb0t/pr0xteus/tree/main/pkg/client) is the public Go package for services that send outbound HTTP through pr0xteus. It requests a lease from the [control API](api.md), dials through the returned SOCKS5 URL, and retries with a fresh proxy when one fails.

```bash
go get github.com/psyb0t/pr0xteus/pkg/client
```

## Example

```go
package main

import (
	"context"
	"log"
	"os"
	"time"

	"github.com/psyb0t/pr0xteus/pkg/client"
)

func main() {
	// Load this from your secret store, never source control.
	token := os.Getenv("PR0XTEUS_API_TOKEN")
	if token == "" {
		log.Fatal("PR0XTEUS_API_TOKEN is required")
	}

	pool := client.NewTunnelPoolClient(
		"http://pr0xteus:8000", 30*time.Second,
		client.WithBearerToken(token),
	)

	c, err := client.New("US", pool, client.WithUserAgent("my-service/1.0"))
	if err != nil {
		log.Fatalf("client.New: %v", err)
	}

	ctx := context.Background()

	// Refuse to start if the proxy does not change the outbound IP.
	if err := c.PreflightSanityCheckWithRetry(ctx); err != nil {
		log.Fatalf("preflight: %v", err)
	}

	resp, err := c.Get(ctx, "https://example.com")
	if err != nil {
		log.Fatalf("Get: %v", err)
	}
	defer resp.Body.Close()
}
```

Construct one `Client` per country at startup and reuse it; each client keeps its own cookie jar.

## Pieces

| Name | What it does |
| --- | --- |
| `NewTunnelPoolClient(baseURL, timeout, opts...)` | The `PoolService` that calls `POST /v1/proxies`. `WithBearerToken` sets the token, `WithPool` requests a named pool instead of the client's country, `WithHTTPClient` swaps the HTTP client. It uses the SOCKS5 URL of each lease. |
| `New(country, pool, opts...)` | Builds the proxy-routed client. `country` is an ISO 3166-1 alpha-2 code and must not be empty. |
| `Client.Do(req)`, `Client.Get(ctx, url)` | Send a request with the retry policy. A request with a body needs `GetBody` set, which `http.NewRequest` does for `bytes.Reader`, `strings.Reader` and `bytes.Buffer`. |
| `WithMode(mode)` | `ModeVPNOnly` (default) or `ModePublicFirst`. |
| `WithRetryPolicy(policy)` | Replaces `DefaultRetryPolicy()`. |
| `WithUserAgent(ua)` | Sets `User-Agent` when the request has none. |
| `PreflightSanityCheck(ctx)`, `PreflightSanityCheckWithRetry(ctx)` | Compare the direct and proxied public IP before serving traffic. |
| `StubPool` | Deterministic `PoolService` for your own tests. |

## Modes

`ModeVPNOnly` sends every request through the proxy. Its transport has no direct dial path, and a direct dial attempt fails with `ErrDirectDialForbidden`.

`ModePublicFirst` tries the host's direct IPv4 connection first. On a `429`, `403` or `503` response, or a transport error, it retries the same request through the proxy with the full retry policy. Use it only where the host's own address is the better first choice, such as an allowlisted API.

## Retries

`DefaultRetryPolicy()` makes up to 5 attempts:

| Attempt | Strategy |
| --- | --- |
| 1 | The original request. |
| 2 | The same proxy over a fresh TCP connection. |
| 3 and 4 | A different proxy (`excludeProxy` set to the one that failed). |
| 5 | A different proxy with `fallbackOk`, so pr0xteus may use the pool's fallback. |

Waits start at 1 second, double each attempt with ±25% jitter, and stop growing at 30 seconds. Transport errors and `429`, `502`, `503` and `504` responses are retried. A cancelled context, `ErrPoolExhausted` and `ErrDirectDialForbidden` stop at once.

## Preflight

`PreflightSanityCheck` asks two IP echo services (`https://api.ipify.org` and `https://ifconfig.me/ip`) for the host's address directly and through the proxy. It returns `ErrPreflightFailed` when it cannot establish either address and `ErrPreflightLeaked` when both are the same. `PreflightSanityCheckWithRetry` retries for up to 10 minutes with 5 to 30 second waits, which covers a cold cell that is still starting. These direct calls leave the host without the proxy, by design.

## Errors

Branch on these with `errors.Is`:

| Error | Meaning |
| --- | --- |
| `ErrEgressUnavailable` | pr0xteus refused or failed to hand out a proxy, or the proxy is unreachable. The target was not dialed directly. |
| `ErrEgressExhausted` | Every attempt in the retry policy failed. |
| `ErrPoolExhausted` | pr0xteus answered `503`; retrying in the same moment will not help. |
| `ErrInvalidCountry` | `New` got an empty country. |
| `ErrDirectDialForbidden` | Something tried to dial directly in `ModeVPNOnly`. |
| `ErrPreflightLeaked`, `ErrPreflightFailed` | Preflight results, see above. |

The package documentation on [pkg.go.dev](https://pkg.go.dev/github.com/psyb0t/pr0xteus/pkg/client) lists every exported name.
