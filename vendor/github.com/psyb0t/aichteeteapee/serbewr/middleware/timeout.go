package middleware

import (
	"bufio"
	"context"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/psyb0t/aichteeteapee"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxscope"
)

// timeoutResponseWriter wraps http.ResponseWriter to prevent concurrent
// writes during timeout.
type timeoutResponseWriter struct {
	BaseResponseWriter
	mu       *sync.Mutex
	header   http.Header
	written  *bool
	timedOut *bool
}

func (tw *timeoutResponseWriter) Header() http.Header {
	return tw.header
}

func (tw *timeoutResponseWriter) WriteHeader(code int) {
	tw.mu.Lock()
	defer tw.mu.Unlock()

	if !*tw.written && !*tw.timedOut {
		tw.copyHeaders()
		*tw.written = true
		tw.ResponseWriter.WriteHeader(code)
	}
}

func (tw *timeoutResponseWriter) Write(data []byte) (int, error) {
	tw.mu.Lock()
	defer tw.mu.Unlock()

	if *tw.timedOut {
		return 0, ctxerrors.Wrap(
			http.ErrHandlerTimeout,
			"write timed-out response",
		)
	}

	if !*tw.written {
		tw.copyHeaders()
		*tw.written = true
	}

	n, err := tw.ResponseWriter.Write(data)
	if err != nil {
		return n, ctxerrors.Wrap(err, "write response")
	}

	return n, nil
}

func (tw *timeoutResponseWriter) Flush() {
	tw.mu.Lock()
	defer tw.mu.Unlock()

	if *tw.timedOut {
		return
	}

	if !*tw.written {
		tw.copyHeaders()
		*tw.written = true
	}

	if flusher, ok := tw.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (tw *timeoutResponseWriter) Hijack() (
	net.Conn,
	*bufio.ReadWriter,
	error,
) {
	tw.mu.Lock()
	defer tw.mu.Unlock()

	if *tw.timedOut {
		return nil, nil, ctxerrors.Wrap(
			http.ErrHandlerTimeout,
			"hijack timed-out response",
		)
	}

	connection, readWriter, err := tw.BaseResponseWriter.Hijack()
	if err != nil {
		return nil, nil, err
	}

	*tw.written = true

	return connection, readWriter, nil
}

func (tw *timeoutResponseWriter) copyHeaders() {
	destination := tw.ResponseWriter.Header()
	clear(destination)

	for name, values := range tw.header {
		destination[name] = append([]string(nil), values...)
	}
}

const (
	// Default timeout durations.
	DefaultTimeout = 10 * time.Second
	ShortTimeout   = 5 * time.Second
	LongTimeout    = 30 * time.Second
)

// TimeoutConfig holds configuration for timeout middleware.
type TimeoutConfig struct {
	Timeout time.Duration
}

type TimeoutOption func(*TimeoutConfig)

func WithTimeout(timeout time.Duration) TimeoutOption {
	return func(c *TimeoutConfig) {
		c.Timeout = timeout
	}
}

func WithDefaultTimeout() TimeoutOption {
	return func(c *TimeoutConfig) {
		c.Timeout = DefaultTimeout
	}
}

func WithShortTimeout() TimeoutOption {
	return func(c *TimeoutConfig) {
		c.Timeout = ShortTimeout
	}
}

func WithLongTimeout() TimeoutOption {
	return func(c *TimeoutConfig) {
		c.Timeout = LongTimeout
	}
}

// TimeoutMiddleware sets a timeout for the request context and handles
// timeout responses.
func Timeout(opts ...TimeoutOption) Middleware {
	config := &TimeoutConfig{
		Timeout: DefaultTimeout,
	}

	for _, opt := range opts {
		opt(config)
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			serveWithTimeout(w, r, next, config.Timeout)
		})
	}
}

func serveWithTimeout(
	w http.ResponseWriter,
	r *http.Request,
	next http.Handler,
	timeout time.Duration,
) {
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()

	doneCh := make(chan struct{})

	var (
		mu               sync.Mutex
		responseWritten  bool
		responseTimedOut bool
	)

	wrappedWriter := &timeoutResponseWriter{
		BaseResponseWriter: BaseResponseWriter{ResponseWriter: w},
		mu:                 &mu,
		header:             w.Header().Clone(),
		written:            &responseWritten,
		timedOut:           &responseTimedOut,
	}

	go func() {
		defer close(doneCh)

		next.ServeHTTP(wrappedWriter, r.WithContext(ctx))
	}()

	select {
	case <-doneCh:
		return
	case <-ctx.Done():
		writeTimeoutResponse(r.Context(), w, wrappedWriter, timeout)
	}
}

func writeTimeoutResponse(
	ctx context.Context,
	w http.ResponseWriter,
	wrappedWriter *timeoutResponseWriter,
	timeout time.Duration,
) {
	wrappedWriter.mu.Lock()
	defer wrappedWriter.mu.Unlock()

	*wrappedWriter.timedOut = true

	if *wrappedWriter.written {
		return
	}

	*wrappedWriter.written = true

	ctxscope.GetLogger(ctx).Warn(
		"request timeout exceeded",
		"timeout", timeout.String(),
	)
	aichteeteapee.WriteJSON(
		w,
		http.StatusGatewayTimeout,
		aichteeteapee.ErrorResponse{
			Code: aichteeteapee.ErrorCodeGatewayTimeout,
			Message: "Gateway timeout - " +
				"request processing took too long",
		},
	)
}
