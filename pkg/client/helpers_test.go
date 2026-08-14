package client

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWaitBackoff(t *testing.T) {
	t.Parallel()

	t.Run("first attempt is a no-op", func(t *testing.T) {
		t.Parallel()

		require.NoError(t, waitBackoff(context.Background(), DefaultRetryPolicy(), 1))
	})

	t.Run("cancelled context aborts the wait", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		err := waitBackoff(ctx, RetryPolicy{
			InitialBackoff:    time.Hour,
			MaxBackoff:        time.Hour,
			BackoffMultiplier: 2,
		}, 2)
		require.ErrorIs(t, err, context.Canceled)
	})
}

func TestRewindBody(t *testing.T) {
	t.Parallel()

	t.Run("nil body needs no rewind", func(t *testing.T) {
		t.Parallel()

		req, err := http.NewRequest(http.MethodGet, "http://example.test", nil)
		require.NoError(t, err)
		require.NoError(t, rewindBody(req))
	})

	t.Run("seekable body is restored after draining", func(t *testing.T) {
		t.Parallel()

		req, err := http.NewRequest(
			http.MethodPost, "http://example.test", strings.NewReader("payload"),
		)
		require.NoError(t, err)

		_, err = io.ReadAll(req.Body)
		require.NoError(t, err)

		require.NoError(t, rewindBody(req))
		data, err := io.ReadAll(req.Body)
		require.NoError(t, err)
		assert.Equal(t, "payload", string(data))
	})

	t.Run("unseekable body cannot retry", func(t *testing.T) {
		t.Parallel()

		req, err := http.NewRequest(
			http.MethodPost, "http://example.test", strings.NewReader("payload"),
		)
		require.NoError(t, err)
		req.GetBody = nil

		require.ErrorIs(t, rewindBody(req), ErrEgressUnavailable)
	})
}

func TestErrorsJoin(t *testing.T) {
	t.Parallel()

	joined := errorsJoin(ErrPoolExhausted, ErrEgressUnavailable)
	assert.ErrorIs(t, joined, ErrPoolExhausted)
	assert.ErrorIs(t, joined, ErrEgressUnavailable)
}
