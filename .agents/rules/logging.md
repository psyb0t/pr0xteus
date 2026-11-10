# Logging and metrics

Prefix: LOG.

- **LOG1:** Log through `ctxscope.GetLogger(ctx)`, so the `binary`, `commit`, `version` and `service` fields travel with each record. Do not use `slog.Default()`, `log` or `fmt.Print` in service code.
- **LOG2:** Never log a bearer token, lease secret, returned proxy URL, WireGuard file content or request body. Log pool, configuration basename, container ID and outcome instead.
- **LOG3:** Prometheus collectors live in `metrics.go` under the `pr0xteus_tunnel_pool_` prefix. Labels stay bounded: pool, outcome and reason only. Never add a configuration name, container ID, client address, destination or request ID as a label.
- **LOG4:** Per-destination traffic belongs to the cell's `/status` view (`internal/pkg/cellproxy/recorder.go`), which caps how many destinations it keeps. It never becomes a Prometheus label.
