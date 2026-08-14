package client

import (
	"context"
	"errors"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestStrategyFor(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name    string
		attempt int
		want    attemptStrategy
	}{
		{name: "first is plain", attempt: 1, want: attemptStrategy{}},
		{name: "second forces fresh tcp", attempt: 2, want: attemptStrategy{freshTCP: true}},
		{name: "third excludes previous", attempt: 3, want: attemptStrategy{excludePrev: true}},
		{name: "fourth excludes previous", attempt: 4, want: attemptStrategy{excludePrev: true}},
		{
			name:    "fifth allows fallback",
			attempt: 5,
			want:    attemptStrategy{excludePrev: true, allowFallback: true},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.want, strategyFor(tc.attempt))
		})
	}
}

func TestRetryPolicyBackoffFor(t *testing.T) {
	t.Parallel()

	policy := RetryPolicy{
		InitialBackoff:    time.Second,
		MaxBackoff:        30 * time.Second,
		BackoffMultiplier: 2,
	}

	assert.Zero(t, policy.backoffFor(1))
	assert.Equal(t, time.Second, policy.backoffFor(2))
	assert.Equal(t, 2*time.Second, policy.backoffFor(3))
	assert.Equal(t, 4*time.Second, policy.backoffFor(4))
	assert.Equal(t, 30*time.Second, policy.backoffFor(20))

	jittered := policy
	jittered.Jitter = 0.25
	got := jittered.backoffFor(2)
	assert.GreaterOrEqual(t, got, time.Duration(0.75*float64(time.Second)))
	assert.LessOrEqual(t, got, time.Duration(1.25*float64(time.Second)))
}

func TestShouldRetry(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string
		resp *http.Response
		err  error
		want bool
	}{
		{name: "context canceled is terminal", err: context.Canceled},
		{name: "deadline exceeded is terminal", err: context.DeadlineExceeded},
		{name: "pool exhausted is terminal", err: ErrPoolExhausted},
		{name: "direct dial forbidden is terminal", err: ErrDirectDialForbidden},
		{name: "net timeout retries", err: &net.DNSError{IsTimeout: true}, want: true},
		{name: "generic transport error retries", err: errors.New("connection reset"), want: true},
		{name: "no response and no error is terminal"},
		{
			name: "bad gateway retries",
			resp: &http.Response{StatusCode: http.StatusBadGateway},
			want: true,
		},
		{
			name: "too many requests retries",
			resp: &http.Response{StatusCode: http.StatusTooManyRequests},
			want: true,
		},
		{name: "ok is terminal", resp: &http.Response{StatusCode: http.StatusOK}},
		{name: "not found is terminal", resp: &http.Response{StatusCode: http.StatusNotFound}},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.want, shouldRetry(tc.resp, tc.err))
		})
	}
}
