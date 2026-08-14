package client

import "errors"

// Sentinel errors. Route caller behavior with errors.Is.
var (
	// ErrEgressUnavailable — pr0xteus refused to assign a SOCKS5
	// URL OR the assigned proxy is unreachable. The single most
	// important sentinel: it means "we couldn't route this request
	// through a VPN tunnel and we did NOT dial the target directly".
	ErrEgressUnavailable = errors.New("egress unavailable")

	// ErrEgressExhausted — every retry attempt in the policy
	// failed. Returned after MaxAttempts is reached without a
	// successful response. The caller decides whether to retry
	// the whole operation later.
	ErrEgressExhausted = errors.New("egress retries exhausted")

	// ErrPoolExhausted — pr0xteus tried every tunnel in the
	// requested pool AND its fallback (if enabled) and has
	// nothing left to offer. Distinct from ErrEgressUnavailable
	// because there's no point retrying further in the same tick.
	ErrPoolExhausted = errors.New("pool exhausted")

	// ErrInvalidCountry — country code didn't map to any pool and
	// no default pool is configured upstream. Config/code bug,
	// not a runtime condition.
	ErrInvalidCountry = errors.New("invalid country / no pool configured")

	// ErrDirectDialForbidden — the proxy-routed transport's
	// DialContext was invoked, which means something tried to
	// escape the SOCKS5 path. Defense in depth.
	ErrDirectDialForbidden = errors.New("direct dial forbidden in vpn-only mode")

	// ErrNoAttemptError — sentinel for the degenerate retry-loop
	// exit where every attempt's recorded error was nil. Should
	// not occur in practice.
	ErrNoAttemptError = errors.New("retry loop ended with no error recorded")

	// ErrPreflightLeaked — preflight found the direct public IP
	// and the proxied public IP are IDENTICAL. The proxy is not
	// actually rewriting outbound traffic. The caller MUST refuse
	// to start in this state.
	ErrPreflightLeaked = errors.New(
		"preflight: direct IP equals proxied IP — proxy not routing",
	)

	// ErrPreflightFailed — preflight could not determine one or
	// both public IPs (network error, IP-echo service down, no
	// quorum across endpoints). Not necessarily a leak, but the
	// proxy can't be proven to work either. Fail closed.
	ErrPreflightFailed = errors.New(
		"preflight: could not establish IP state",
	)
)
