package client

// math/rand/v2 is intentional: backoff jitter is decoration on top
// of an already-correct retry policy; no crypto-strength entropy
// needed.
import (
	"context"
	"errors"
	"math/rand/v2"
	"net"
	"net/http"
	"time"
)

// RetryPolicy controls Client retry behavior.
type RetryPolicy struct {
	// MaxAttempts is the total number of attempts including the
	// first. Default 5.
	MaxAttempts int

	// InitialBackoff is the wait between attempt 1 and 2.
	InitialBackoff time.Duration

	// MaxBackoff caps the exponential growth so attempt 5's wait
	// doesn't run away. 30s default.
	MaxBackoff time.Duration

	// BackoffMultiplier is the per-attempt growth factor (2.0 →
	// 1s, 2s, 4s, 8s, 16s...). The cap lives on MaxBackoff.
	BackoffMultiplier float64

	// Jitter is the ±fraction applied to each backoff. 0.25 means
	// "±25%". Required to avoid thundering-herd on simultaneous
	// retries.
	Jitter float64

	// UseFallbackPool toggles whether the LAST attempt is allowed
	// to escalate to the pool's configured fallback. Default true.
	UseFallbackPool bool
}

// DefaultRetryPolicy returns the canonical 5-attempt staircase.
func DefaultRetryPolicy() RetryPolicy {
	return RetryPolicy{
		MaxAttempts:       defaultMaxAttempts,
		InitialBackoff:    defaultInitialBackoff,
		MaxBackoff:        defaultMaxBackoff,
		BackoffMultiplier: defaultBackoffMultiplier,
		Jitter:            defaultJitter,
		UseFallbackPool:   true,
	}
}

const (
	defaultMaxAttempts       = 5
	defaultInitialBackoff    = 1 * time.Second
	defaultMaxBackoff        = 30 * time.Second
	defaultBackoffMultiplier = 2.0
	defaultJitter            = 0.25
)

// attemptStrategy maps a 1-indexed attempt number to the recovery
// strategy that attempt uses:
//
//	attempt 1 → original request, no exclusions
//	attempt 2 → same proxy, fresh TCP (force no-keepalive)
//	attempts 3+4 → ask pool for a DIFFERENT proxy
//	attempt 5 → ask pool with fallback_ok=true (neighbor pool)
type attemptStrategy struct {
	// freshTCP forces a brand-new TCP connection (no idle-pool
	// reuse from the previous attempt). Applies to attempt 2.
	freshTCP bool

	// excludePrev passes the previous proxy URL to the pool so it
	// picks a different tunnel. Applies to attempts 3-5.
	excludePrev bool

	// allowFallback lets the pool escalate to the fallback pool.
	// Applies to attempt 5 only.
	allowFallback bool
}

func strategyFor(attempt int) attemptStrategy {
	switch {
	case attempt <= 1:
		return attemptStrategy{}
	case attempt == 2: //nolint:mnd
		return attemptStrategy{freshTCP: true}
	case attempt <= 4: //nolint:mnd
		return attemptStrategy{excludePrev: true}
	default:
		return attemptStrategy{excludePrev: true, allowFallback: true}
	}
}

// backoffFor returns the wait time before the given 1-indexed
// attempt. Attempt 1 has zero backoff. Subsequent attempts grow
// exponentially with jitter, capped at MaxBackoff.
func (p RetryPolicy) backoffFor(attempt int) time.Duration {
	if attempt <= 1 {
		return 0
	}

	// Exponential growth: initial * mult^(attempt-2)
	base := float64(p.InitialBackoff)
	for range attempt - 2 {
		base *= p.BackoffMultiplier
	}

	if base > float64(p.MaxBackoff) {
		base = float64(p.MaxBackoff)
	}

	// Jitter: ±Jitter fraction. rand.Float64 gives [0, 1),
	// shift to [-1, 1) then scale.
	if p.Jitter > 0 {
		shift := (rand.Float64()*2 - 1) * p.Jitter //nolint:gosec
		base *= 1 + shift
	}

	return time.Duration(base)
}

// shouldRetry classifies an error or HTTP response as transient
// (retry) vs permanent (give up immediately).
func shouldRetry(resp *http.Response, err error) bool {
	if err != nil {
		// ctx cancel/deadline = caller wants to stop; never retry.
		if errors.Is(err, context.Canceled) ||
			errors.Is(err, context.DeadlineExceeded) {
			return false
		}

		// Pool exhausted = nothing left to try; give up.
		if errors.Is(err, ErrPoolExhausted) {
			return false
		}

		// Direct-dial forbidden fires inside the hardening itself;
		// it's a code bug, not a transient condition.
		if errors.Is(err, ErrDirectDialForbidden) {
			return false
		}

		// net.OpError + timeout: retryable.
		var netErr net.Error
		if errors.As(err, &netErr) && netErr.Timeout() {
			return true
		}

		// Everything else with an error counts as transient — most
		// likely a TCP failure from the proxy or the tunnel. Retry.
		return true
	}

	if resp == nil {
		return false
	}

	// HTTP status codes that justify retry.
	switch resp.StatusCode {
	case http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout,
		http.StatusTooManyRequests:
		return true
	}

	return false
}
