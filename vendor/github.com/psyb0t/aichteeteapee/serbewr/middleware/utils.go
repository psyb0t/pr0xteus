package middleware

import (
	"net"
	"net/http"
	"strings"

	"github.com/psyb0t/aichteeteapee"
)

// GetRequestID extracts the request ID from the request context.
func GetRequestID(r *http.Request) string {
	if requestID, ok := r.Context().Value(
		aichteeteapee.ContextKeyRequestID,
	).(string); ok {
		return requestID
	}

	return ""
}

// GetClientIP extracts the client IP address from the request.
func GetClientIP(r *http.Request) string {
	if xff := r.Header.Get(aichteeteapee.HeaderNameXForwardedFor); xff != "" {
		// X-Forwarded-For can contain multiple IPs, take the first one
		if before, _, ok := strings.Cut(xff, ","); ok {
			return strings.TrimSpace(before)
		}

		return strings.TrimSpace(xff)
	}

	if xri := r.Header.Get(aichteeteapee.HeaderNameXRealIP); xri != "" {
		return strings.TrimSpace(xri)
	}

	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}

	return ip
}
