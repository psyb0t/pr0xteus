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
	"strconv"
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
	defaultProxyLimit = 100
	maxProxyLimit     = 1000
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
	mux.HandleFunc(http.MethodGet+" "+pathV1Proxies, s.authenticate(s.handleProxies))
	mux.HandleFunc(http.MethodGet+" "+pathV1Pools, s.authenticate(s.handlePools))
	mux.HandleFunc(http.MethodGet+" "+pathV1Cells, s.authenticate(s.handleCells))
	mux.HandleFunc(http.MethodGet+" "+pathV1CellByID, s.authenticate(s.handleCell))
	mux.HandleFunc(http.MethodDelete+" "+pathV1CellByID, s.authenticate(s.handleDeleteCell))
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

	excludeProxy, ok := s.parseExcludedProxy(w, request.ExcludeProxy)
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

	lease, err := s.mgr.IssueLease(acq)
	if err != nil {
		s.mgr.Release(acq)
		ctxscope.GetLogger(r.Context()).Error("proxy lease issue failed", "err", err)
		writeError(w, http.StatusInternalServerError, aichteeteapee.ErrorResponseInternalServerError)

		return
	}

	response := ProxyResponse{
		URL:         lease.URL,
		Pool:        acq.Pool,
		ExitCountry: acq.Tunnel.ExitCountry,
		ExitIP:      acq.Tunnel.ExitIP,
		ExpiresAt:   lease.ExpiresAt,
	}

	ctxscope.GetLogger(r.Context()).Info(
		"proxy assigned",
		"country", request.Country,
		"pool", acq.Pool,
		"exit_country", response.ExitCountry,
	)
	aichteeteapee.WriteJSON(w, http.StatusOK, response)
	TunnelAcquireTotal.WithLabelValues(acq.Pool, metricOutcomeOK).Inc()

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

func (s *APIServer) parseExcludedProxy(w http.ResponseWriter, raw string) (*url.URL, bool) {
	if raw == "" {
		return nil, true
	}

	proxyURL, err := s.mgr.ResolveExcludedProxy(raw)
	if err != nil {
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

// handlePools returns the bounded authenticated operator view of current pool
// state. Pools are local configuration, so the total is an exact in-memory
// count.
func (s *APIServer) handlePools(w http.ResponseWriter, r *http.Request) {
	limit, offset, ok := collectionPage(w, r)
	if !ok {
		return
	}

	pools, offset, total := pageItems(s.mgr.Views(), limit, offset)
	aichteeteapee.WriteJSON(w, http.StatusOK, PoolListResponse{
		Pools:  pools,
		Limit:  limit,
		Offset: offset,
		Total:  total,
	})
}

// handleProxies returns the flattened active-proxy inventory. It never creates
// a tunnel or a new credential: POST remains the allocation action.
func (s *APIServer) handleProxies(w http.ResponseWriter, r *http.Request) {
	limit, offset, ok := collectionPage(w, r)
	if !ok {
		return
	}

	proxies, offset, total := pageItems(s.mgr.ProxyViews(), limit, offset)

	aichteeteapee.WriteJSON(w, http.StatusOK, ProxyListResponse{
		Proxies: proxies,
		Limit:   limit,
		Offset:  offset,
		Total:   total,
	})
}

// collectionPage parses the shared offset pagination contract for every
// control-plane collection.
func collectionPage(w http.ResponseWriter, r *http.Request) (int, int, bool) {
	limit, err := boundedQueryInt(r, "limit", defaultProxyLimit, maxProxyLimit)
	if err != nil {
		writeError(w, http.StatusBadRequest, aichteeteapee.ErrorResponseValidationFailed)

		return 0, 0, false
	}

	offset, err := boundedQueryInt(r, "offset", 0, 0)
	if err != nil {
		writeError(w, http.StatusBadRequest, aichteeteapee.ErrorResponseValidationFailed)

		return 0, 0, false
	}

	return limit, offset, true
}

// pageItems returns a safe page and normalizes an offset beyond the end to the
// empty terminal page. All control-plane collections are bounded snapshots, so
// total is exact and cheap to compute.
func pageItems[T any](items []T, limit, offset int) ([]T, int, int) {
	total := len(items)
	if offset > total {
		offset = total
	}

	return items[offset:min(offset+limit, total)], offset, total
}

func boundedQueryInt(r *http.Request, name string, defaultValue, maximum int) (int, error) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return defaultValue, nil
	}

	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 || maximum > 0 && value > maximum ||
		name == "limit" && value == 0 {
		return 0, errors.New("invalid pagination value")
	}

	return value, nil
}

func writeError(
	w http.ResponseWriter, status int, response aichteeteapee.ErrorResponse,
) {
	aichteeteapee.WriteJSON(w, status, response)
}
