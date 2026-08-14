package client

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/psyb0t/ctxerrors"
)

// waitBackoff sleeps for the policy's backoff between attempts,
// aborting if the context is cancelled. Attempt 1's backoff is
// zero, so this is a no-op on the first iteration.
func waitBackoff(
	ctx context.Context, policy RetryPolicy, attempt int,
) error {
	backoff := policy.backoffFor(attempt)
	if backoff <= 0 {
		return nil
	}

	timer := time.NewTimer(backoff)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctxerrors.Wrap(
			ctx.Err(), "egress retry backoff aborted",
		)
	case <-timer.C:
		return nil
	}
}

// rewindBody restores req.Body for a retry via req.GetBody.
// http.NewRequest sets GetBody automatically for bytes.Reader,
// strings.Reader, and bytes.Buffer bodies.
//
// Returns nil for body-less requests (GET, HEAD, DELETE, etc.).
// Returns ErrEgressUnavailable when the body exists but is not
// seekable — the caller chose an unseekable reader and cannot
// reasonably be retried.
func rewindBody(req *http.Request) error {
	if req.Body == nil {
		return nil
	}

	if req.GetBody == nil {
		return ctxerrors.Wrap(
			ErrEgressUnavailable,
			"request body is not seekable; cannot retry",
		)
	}

	body, err := req.GetBody()
	if err != nil {
		return ctxerrors.Wrap(err, "GetBody")
	}

	req.Body = body

	return nil
}

// errorsJoin is a tiny wrapper so client.go doesn't need to
// import "errors" for one call.
func errorsJoin(errs ...error) error {
	return errors.Join(errs...)
}
