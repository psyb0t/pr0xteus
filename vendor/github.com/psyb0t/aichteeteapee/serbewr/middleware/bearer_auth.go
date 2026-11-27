package middleware

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"path"
	"strings"

	"github.com/psyb0t/aichteeteapee"
	"github.com/psyb0t/ctxscope"
)

// BearerAuthConfig holds configuration for bearer-token auth middleware.
type BearerAuthConfig struct {
	// Tokens is the set of accepted bearer tokens. The common case is a
	// single API key read from an env var.
	Tokens          map[string]bool
	UnauthorizedMsg string
	// Validator, when set, fully replaces the Tokens check.
	Validator func(token string) bool
	SkipPaths map[string]bool
	// UseConstantTime compares tokens in constant time to prevent timing
	// attacks.
	UseConstantTime bool
}

type BearerAuthOption func(*BearerAuthConfig)

// WithBearerAuthTokens adds accepted bearer tokens. Empty strings are
// ignored so an unset env var never becomes a valid token. The common case
// is a single API key: WithBearerAuthTokens(cfg.APIToken).
func WithBearerAuthTokens(tokens ...string) BearerAuthOption {
	return func(c *BearerAuthConfig) {
		if c.Tokens == nil {
			c.Tokens = make(map[string]bool)
		}

		for _, token := range tokens {
			if token != "" {
				c.Tokens[token] = true
			}
		}
	}
}

// WithBearerAuthValidator sets a custom validation function that fully
// replaces the token-set check.
func WithBearerAuthValidator(
	validator func(token string) bool,
) BearerAuthOption {
	return func(c *BearerAuthConfig) {
		c.Validator = validator
	}
}

// WithBearerAuthSkipPaths sets paths that bypass authentication.
func WithBearerAuthSkipPaths(paths ...string) BearerAuthOption {
	return func(c *BearerAuthConfig) {
		if c.SkipPaths == nil {
			c.SkipPaths = make(map[string]bool)
		}

		for _, p := range paths {
			c.SkipPaths[p] = true
		}
	}
}

// WithBearerAuthUnauthorizedMessage sets the message in the 401 JSON body.
func WithBearerAuthUnauthorizedMessage(
	message string,
) BearerAuthOption {
	return func(c *BearerAuthConfig) {
		c.UnauthorizedMsg = message
	}
}

// WithBearerAuthConstantTimeComparison toggles constant-time token
// comparison. It defaults to true; disable it only to trade timing-attack
// resistance for speed on non-secret tokens.
func WithBearerAuthConstantTimeComparison(enable bool) BearerAuthOption {
	return func(c *BearerAuthConfig) {
		c.UseConstantTime = enable
	}
}

// BearerAuth authenticates requests carrying "Authorization: Bearer <token>"
// against a set of accepted tokens (or a custom validator), and answers a
// JSON error envelope on failure — the right shape for an API, unlike
// BasicAuth's browser-popup challenge. The common use is a single API key
// read from an env var and passed via WithBearerAuthTokens.
func BearerAuth(opts ...BearerAuthOption) Middleware {
	config := &BearerAuthConfig{
		Tokens:          make(map[string]bool),
		UnauthorizedMsg: aichteeteapee.DefaultUnauthorizedMessage,
		Validator:       nil,
		SkipPaths:       make(map[string]bool),
		UseConstantTime: true,
	}

	for _, opt := range opts {
		opt(config)
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// path.Clean normalizes the URL path for the configured skip-list
			// lookup. This is net/url path.Clean, not filesystem-traversal
			// sanitizing, so the rule does not apply.
			// nosemgrep
			if config.SkipPaths[path.Clean(r.URL.Path)] {
				next.ServeHTTP(w, r)

				return
			}

			token, ok := bearerToken(r)
			if !ok {
				ctxscope.GetLogger(r.Context()).Warn(
					"missing bearer token",
				)
				bearerUnauthorized(w, config)

				return
			}

			if !authenticateToken(config, token) {
				ctxscope.GetLogger(r.Context()).Warn(
					"bearer auth failed",
				)
				bearerUnauthorized(w, config)

				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// bearerToken extracts the token from a case-insensitive "Bearer " scheme.
func bearerToken(r *http.Request) (string, bool) {
	header := r.Header.Get(aichteeteapee.HeaderNameAuthorization)

	scheme := aichteeteapee.AuthSchemeBearer
	if len(header) <= len(scheme) ||
		!strings.EqualFold(header[:len(scheme)], scheme) {
		return "", false
	}

	return header[len(scheme):], true
}

// authenticateToken validates the token against the custom validator, or the
// configured token set when no validator is set.
func authenticateToken(config *BearerAuthConfig, token string) bool {
	if config.Validator != nil {
		return config.Validator(token)
	}

	if len(config.Tokens) == 0 {
		return false
	}

	if config.UseConstantTime {
		return constantTimeTokenAuth(config.Tokens, token)
	}

	return config.Tokens[token]
}

// constantTimeTokenAuth compares the provided token against every accepted
// token WITHOUT an early exit, so neither which token matched nor how far
// the comparison got leaks through timing.
func constantTimeTokenAuth(tokens map[string]bool, provided string) bool {
	providedHash := sha256.Sum256([]byte(provided))

	matched := false

	for token := range tokens {
		expectedHash := sha256.Sum256([]byte(token))
		if subtle.ConstantTimeCompare(
			expectedHash[:], providedHash[:],
		) == 1 {
			matched = true
		}
	}

	return matched
}

// bearerUnauthorized writes the 401 JSON error envelope.
func bearerUnauthorized(w http.ResponseWriter, config *BearerAuthConfig) {
	aichteeteapee.WriteJSON(
		w,
		http.StatusUnauthorized,
		aichteeteapee.ErrorResponse{
			Code:    aichteeteapee.ErrorCodeUnauthorized,
			Message: config.UnauthorizedMsg,
		},
	)
}
