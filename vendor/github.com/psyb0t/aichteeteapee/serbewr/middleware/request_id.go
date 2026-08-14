package middleware

import (
	"context"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/psyb0t/aichteeteapee"
	"github.com/psyb0t/ctxscope"
)

const (
	maxRequestIDLength    = 128
	requestIDAllowedRunes = "abcdefghijklmnopqrstuvwxyz" +
		"ABCDEFGHIJKLMNOPQRSTUVWXYZ" +
		"0123456789._-"
)

func isValidRequestID(id string) bool {
	if len(id) > maxRequestIDLength {
		return false
	}

	for _, c := range id {
		if !strings.ContainsRune(requestIDAllowedRunes, c) {
			return false
		}
	}

	return true
}

func RequestID() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(
			func(
				w http.ResponseWriter,
				r *http.Request,
			) {
				reqID := r.Header.Get(
					aichteeteapee.HeaderNameXRequestID,
				)
				if reqID == "" || !isValidRequestID(reqID) {
					reqID = uuid.New().String()
				}

				w.Header().Set(
					aichteeteapee.HeaderNameXRequestID,
					reqID,
				)

				ctx := context.WithValue(
					r.Context(),
					aichteeteapee.ContextKeyRequestID,
					reqID,
				)

				// On the scope rather than a ctx-pinned logger, so it also
				// crosses a process hop via scope.ToJSON and shows up on
				// every line any downstream package logs.
				ctx = ctxscope.Set(
					ctx,
					ctxscope.Attr(aichteeteapee.FieldRequestID, reqID),
				)

				next.ServeHTTP(w, r.WithContext(ctx))
			},
		)
	}
}
