package pr0xteus

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"

	"github.com/psyb0t/aichteeteapee"
	"github.com/psyb0t/ctxscope"
)

const (
	apiV1Prefix       = "/v1"
	pathV1Proxies     = apiV1Prefix + "/proxies"
	pathV1Pools       = apiV1Prefix + "/pools"
	bearerScheme      = "Bearer "
	maxRequestBody    = 16 * 1024
	countryCodeLength = 2
)

// APIServer exposes the authenticated control plane. It keeps only the
// SHA-256 digest of the bearer token after construction so normal request
// handling never retains the operator's raw secret.
type APIServer struct {
	mgr         *Manager
	tokenDigest [sha256.Size]byte
}

// NewAPIServer returns the versioned API server backed by mgr.
func NewAPIServer(mgr *Manager, token []byte) *APIServer {
	return &APIServer{
		mgr:         mgr,
		tokenDigest: sha256.Sum256(token),
	}
}

// Register wires every route onto mux. The control-plane routes all require
// bearer authentication; liveness and metrics deliberately live on their
// separate, unversioned listener.
func (s *APIServer) Register(mux *http.ServeMux) {
	mux.HandleFunc(http.MethodPost+" "+pathV1Proxies, s.authenticate(s.handleProxy))
	mux.HandleFunc(http.MethodGet+" "+pathV1Pools, s.authenticate(s.handlePools))
}

// Mux returns a stdlib handler with every API route wired. It is also a small
// test seam for callers embedding pr0xteus.
func (s *APIServer) Mux() *http.ServeMux {
	mux := http.NewServeMux()
	s.Register(mux)

	return mux
}

// ServeHTTP makes APIServer an http.Handler.
func (s *APIServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.Mux().ServeHTTP(w, r)
}

// ProxyRequest is the public request payload for POST /v1/proxies. Specify
// exactly one of country or pool. excludeProxy is a prior SOCKS5 URL that the
// manager must avoid when selecting a replacement.
type ProxyRequest struct {
	Country      string `json:"country,omitempty"`
	Pool         string `json:"pool,omitempty"`
	ExcludeProxy string `json:"excludeProxy,omitempty"`
	FallbackOK   bool   `json:"fallbackOk,omitempty"`
}

// handleProxy assigns a live SOCKS5 URL from a country-routed or explicit
// pool. A body rather than query parameters keeps retry metadata out of URLs
// and gives the contract one strict, versioned shape.
func (s *APIServer) handleProxy(w http.ResponseWriter, r *http.Request) {
	request, ok := decodeProxyRequest(w, r)
	if !ok {
		return
	}

	excludeProxy, ok := parseExcludedProxy(w, request.ExcludeProxy)
	if !ok {
		return
	}

	acq, err := s.acquire(
		r.Context(), request.Country, request.Pool, excludeProxy, request.FallbackOK,
	)
	if err != nil {
		s.writeAcquireError(r.Context(), w, err)

		return
	}

	response := ProxyResponse{
		Pool:        acq.Pool,
		ExitCountry: acq.Tunnel.ExitCountry,
		ExitIP:      acq.Tunnel.ExitIP,
	}
	if acq.Tunnel.ProxyURL != nil {
		response.URL = acq.Tunnel.ProxyURL.String()
	}

	ctxscope.GetLogger(r.Context()).Info(
		"proxy assigned",
		"country", request.Country,
		"pool", acq.Pool,
		"exit_country", response.ExitCountry,
	)
	aichteeteapee.WriteJSON(w, http.StatusOK, response)

	// The manager tracks selection only, not downstream SOCKS5 session lifetime.
	// LastUsedAt keeps a just-returned tunnel warm for the configured idle window.
	s.mgr.Release(acq)
}

func (s *APIServer) authenticate(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, ok := strings.CutPrefix(
			r.Header.Get(aichteeteapee.HeaderNameAuthorization), bearerScheme,
		)
		if !ok || token == "" {
			writeError(w, http.StatusUnauthorized, aichteeteapee.ErrorResponseUnauthorized)

			return
		}

		providedDigest := sha256.Sum256([]byte(token))
		if subtle.ConstantTimeCompare(s.tokenDigest[:], providedDigest[:]) != 1 {
			writeError(w, http.StatusUnauthorized, aichteeteapee.ErrorResponseUnauthorized)

			return
		}

		next(w, r)
	}
}

func decodeProxyRequest(w http.ResponseWriter, r *http.Request) (ProxyRequest, bool) {
	if !hasJSONContentType(r) {
		writeError(w, http.StatusUnsupportedMediaType, aichteeteapee.ErrorResponseUnsupportedContentType)

		return ProxyRequest{}, false
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)

	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()

	var request ProxyRequest
	if err := decoder.Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, aichteeteapee.ErrorResponseValidationFailed)

		return ProxyRequest{}, false
	}

	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, aichteeteapee.ErrorResponseValidationFailed)

		return ProxyRequest{}, false
	}

	hasCountry := strings.TrimSpace(request.Country) != ""

	hasPool := strings.TrimSpace(request.Pool) != ""
	if hasCountry == hasPool {
		writeError(w, http.StatusBadRequest, aichteeteapee.ErrorResponseValidationFailed)

		return ProxyRequest{}, false
	}

	if hasCountry && !isCountryCode(request.Country) {
		writeError(w, http.StatusBadRequest, aichteeteapee.ErrorResponseValidationFailed)

		return ProxyRequest{}, false
	}

	return request, true
}

func hasJSONContentType(r *http.Request) bool {
	contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))

	return err == nil && contentType == aichteeteapee.ContentTypeJSON
}

func parseExcludedProxy(w http.ResponseWriter, raw string) (*url.URL, bool) {
	if raw == "" {
		return nil, true
	}

	proxyURL, err := url.ParseRequestURI(raw)
	if err != nil || proxyURL.Scheme != proxySchemeSOCKS5 || proxyURL.Host == "" ||
		proxyURL.User != nil || proxyURL.Port() == "" {
		writeError(w, http.StatusBadRequest, aichteeteapee.ErrorResponseValidationFailed)

		return nil, false
	}

	return proxyURL, true
}

func isCountryCode(country string) bool {
	trimmed := strings.TrimSpace(country)
	if len(trimmed) != countryCodeLength {
		return false
	}

	for index := range len(trimmed) {
		if !isASCIIAlpha(trimmed[index]) {
			return false
		}
	}

	return true
}

func isASCIIAlpha(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z'
}

// acquire dispatches to AcquireForPool for an explicit operator override,
// otherwise to the configured country router.
func (s *APIServer) acquire(
	ctx context.Context, country, pool string,
	excludeProxy *url.URL, fallbackOK bool,
) (Acquisition, error) {
	if pool != "" {
		return s.mgr.AcquireForPool(ctx, pool, excludeProxy, fallbackOK)
	}

	return s.mgr.AcquireForCountry(ctx, country, excludeProxy, fallbackOK)
}

func (s *APIServer) writeAcquireError(
	ctx context.Context, w http.ResponseWriter, err error,
) {
	switch {
	case errors.Is(err, ErrInvalidCountry), errors.Is(err, ErrUnknownPool):
		writeError(w, http.StatusBadRequest, aichteeteapee.ErrorResponseValidationFailed)
	case errors.Is(err, ErrPoolExhausted), errors.Is(err, ErrPoolUnavailable):
		writeError(w, http.StatusServiceUnavailable, aichteeteapee.ErrorResponseServiceUnavailable)
	default:
		ctxscope.GetLogger(ctx).Error("proxy acquisition failed", "err", err)
		writeError(w, http.StatusInternalServerError, aichteeteapee.ErrorResponseInternalServerError)
	}
}

// handlePools returns the authenticated operator view of current pool state.
func (s *APIServer) handlePools(w http.ResponseWriter, _ *http.Request) {
	aichteeteapee.WriteJSON(w, http.StatusOK, map[string]any{
		"pools": s.mgr.Views(),
	})
}

func writeError(
	w http.ResponseWriter, status int, response aichteeteapee.ErrorResponse,
) {
	aichteeteapee.WriteJSON(w, status, response)
}
