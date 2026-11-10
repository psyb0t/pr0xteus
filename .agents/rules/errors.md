# Errors

Prefix: ERR.

- **ERR1:** Package sentinels live in `errors.go` beside the code that raises them (`internal/pkg/services/pr0xteus/errors.go`, `pkg/client/errors.go`). Callers branch with `errors.Is`. Check `ctxerrors/commerr` before adding a sentinel; `DestroyCell` already uses `commerr.ErrNotFound`.
- **ERR2:** Wrap returned errors with `ctxerrors.Wrap` or `Wrapf` and a short action phrase. Wrap configuration failures around `ErrConfigInvalid` or `ErrBundleConfMissing` and name the offending file, pool or setting, so `config check` and startup print a fixable message.
- **ERR3:** The HTTP mapping lives in `writeAcquireError` and the cell handlers: `ErrInvalidCountry` and `ErrUnknownPool` are `400`, `ErrPoolExhausted` and `ErrPoolUnavailable` are `503`, everything else is `500`. A new sentinel a client can hit gets an explicit case and a test.
- **ERR4:** `pkg/client` wraps every control-plane failure around `ErrEgressUnavailable` (or `ErrPoolExhausted` for a 503 envelope) and never falls back to a direct dial in `ModeVPNOnly`. Keep that fail-closed behavior.
