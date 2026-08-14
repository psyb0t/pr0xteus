// Package client — usage example.
//
//	import (
//	    "context"
//	    "log"
//	    "os"
//	    "time"
//
//	    "github.com/psyb0t/pr0xteus/pkg/client"
//	)
//
//	func main() {
//	    // Load this from your secret store, never source control.
//	    token := os.Getenv("PR0XTEUS_API_TOKEN")
//	    if token == "" {
//	        log.Fatal("PR0XTEUS_API_TOKEN is required")
//	    }
//
//	    // PoolService backed by the private pr0xteus control API.
//	    pool := client.NewTunnelPoolClient(
//	        "http://pr0xteus:8000", 30*time.Second,
//	        client.WithPool("western_eu"),
//	        client.WithBearerToken(token),
//	    )
//
//	    // Build the proxy-routed HTTP client. ModeVPNOnly is the default:
//	    // every request goes through SOCKS5; direct dials are refused.
//	    c, err := client.New("de", pool,
//	        client.WithUserAgent("my-service/1.0"),
//	    )
//	    if err != nil {
//	        log.Fatalf("egress.New: %v", err)
//	    }
//
//	    // Refuse to proceed if the proxy does not rewrite the outbound IP.
//	    ctx := context.Background()
//	    if err := c.PreflightSanityCheckWithRetry(ctx); err != nil {
//	        log.Fatalf("preflight: %v", err)
//	    }
//
//	    // Use it like any *http.Client.
//	    resp, err := c.Get(ctx, "https://example.com")
//	    if err != nil {
//	        log.Fatalf("Get: %v", err)
//	    }
//	    defer resp.Body.Close()
//	}
//
// For sources where the host's public IP buys preferential
// treatment (e.g. registered API feeder), opt in to public-first:
//
//	c, _ := client.New("de", pool, client.WithMode(client.ModePublicFirst))
//
// In ModePublicFirst, the first attempt goes direct; on 429 / 403
// / 503 / transport-error the same request retries through the
// SOCKS5 proxy with the full retry policy.
package client
