package middleware

import (
	"bufio"
	"net"
	"net/http"

	"github.com/psyb0t/ctxerrors"
)

// BaseResponseWriter provides a base implementation that supports hijacking for
// WebSocket upgrades
// Other middleware can embed this to get hijacking support for free.
type BaseResponseWriter struct {
	http.ResponseWriter
}

var (
	_ http.Flusher  = (*BaseResponseWriter)(nil)
	_ http.Hijacker = (*BaseResponseWriter)(nil)
)

// Hijack implements http.Hijacker interface for WebSocket support.
func (brw *BaseResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if hijacker, ok := brw.ResponseWriter.(http.Hijacker); ok {
		conn, rw, err := hijacker.Hijack()
		if err != nil {
			return nil, nil, ctxerrors.Wrap(err, "failed to hijack connection")
		}

		return conn, rw, nil
	}

	return nil, nil, http.ErrNotSupported
}

// Unwrap exposes the underlying ResponseWriter so http.ResponseController can
// reach the real writer's Flusher/Hijacker/etc. through any wrapper that
// embeds BaseResponseWriter. Without it, streaming (SSE) behind middleware
// that wraps the writer (logger, timeout) buffers, because the wrapper does
// not satisfy http.Flusher.
func (brw *BaseResponseWriter) Unwrap() http.ResponseWriter {
	return brw.ResponseWriter
}

// Flush implements http.Flusher by delegating to the underlying writer when it
// supports flushing. Embedding the http.ResponseWriter interface does not
// promote Flush (it is not part of that interface), so wrappers embedding
// BaseResponseWriter would otherwise silently drop flushing on streamed
// responses.
func (brw *BaseResponseWriter) Flush() {
	if flusher, ok := brw.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}
