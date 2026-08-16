package pr0xteus

import (
	"net/url"
	"time"
)

// TunnelState is the lifecycle phase a tunnel sits in. Encodes the
// state-machine cycle: spawning → hot → unhealthy → reaping → gone.
type TunnelState string

const (
	// TunnelStateSpawning — docker run issued; waiting for cell to
	// report a wireguard handshake. No requests should reach this
	// tunnel until it transitions to hot.
	TunnelStateSpawning TunnelState = "spawning"

	// TunnelStateHot — cell running, wg handshake recent, ready
	// for requests.
	TunnelStateHot TunnelState = "hot"

	// TunnelStateUnhealthy — wg handshake stale or container errored.
	// Will be reaped + respawned by the health monitor on its next
	// tick. No new requests routed here.
	TunnelStateUnhealthy TunnelState = "unhealthy"

	// TunnelStateReaping — docker stop in progress (idle or
	// unhealthy). Terminal state from the pool's perspective.
	TunnelStateReaping TunnelState = "reaping"
)

// Tunnel is one running (or pending) cell container. The pool
// holds at most one per pool name in v1; v3 may grow N.
type Tunnel struct {
	// ContainerID is the docker container ID returned by the runner.
	// Empty during spawn until the docker daemon assigns one.
	ContainerID string

	// ConfName is the bundled .conf basename (without extension)
	// this tunnel was spawned from. Carried through to telemetry
	// + the recently-failed cache.
	ConfName string

	// ProxyURL is the cell's private SOCKS5 address. It is internal routing
	// state, never the controller-fronted client URL. Empty while spawning.
	ProxyURL *url.URL

	// GatewayAddr is the controller-reachable cell SOCKS5 address. In the
	// contained Compose topology it is the cell-control-network IP, never the
	// caller-facing controller address.
	GatewayAddr string

	// State is the lifecycle phase, see TunnelState.
	State TunnelState

	// Pool is the logical pool name this tunnel belongs to (matches
	// a key in pools.yaml).
	Pool string

	// ExitCountry is the ISO 3166-1 alpha-2 exit location. Operators may set
	// it explicitly per config; conventional <cc>-<location> names are a
	// backwards-compatible fallback.
	ExitCountry string

	// ExitIP is reserved optional egress metadata. The controller does not
	// currently perform an external exit-IP lookup, so it is normally empty.
	ExitIP string

	// SpawnedAt is when the container's docker run returned. Used
	// for spawn-budget calculations.
	SpawnedAt time.Time

	// HealthyAt is the most recent timestamp the wireguard handshake
	// was confirmed fresh. Drives reap + unhealthy decisions.
	HealthyAt time.Time

	// LastUsedAt is the most recent timestamp a proxy assignment
	// returned this tunnel's URL. Drives idle-reap.
	LastUsedAt time.Time

	// LastURL is the most recently issued controller-fronted SOCKS5 URL. It
	// contains a short-lived credential and stays in memory only.
	LastURL string

	// LastURLExpiresAt bounds reuse of LastURL.
	LastURLExpiresAt time.Time

	// InFlight is the count of proxy assignments that returned this
	// tunnel and haven't yet completed their HTTP exchange. The
	// reaper refuses to kill a tunnel with InFlight > 0.
	InFlight int
}

// PoolView is the admin JSON shape returned by GET /v1/pools — one
// entry per known pool, including the current tunnel state.
type PoolView struct {
	// Name is the logical pool name.
	Name string `json:"name"`

	// Region is the human-facing region tag from pools.yaml
	// (e.g. "eastern_europe" → western/eastern_europe/middle_east/etc).
	Region string `json:"region"`

	// Tunnel is the current tunnel for this pool, or nil when no
	// tunnel is spawned (cold pool).
	Tunnel *TunnelView `json:"tunnel,omitempty"`

	// FallbackPool is the pool a client's last-attempt
	// retry escalates to, when this pool's primary is exhausted.
	FallbackPool string `json:"fallbackPool,omitempty"`

	// ConfCount is the number of distinct .conf files available
	// in this pool. Helps operators see whether a pool has rotated
	// through every option recently.
	ConfCount int `json:"confCount"`

	// RecentlyFailed is the count of .conf files currently in the
	// pool's failure cache.
	RecentlyFailed int `json:"recentlyFailed"`
}

// TunnelView is the JSON projection of Tunnel for the operator API. It omits
// Docker identifiers and in-flight bookkeeping because they are internal
// implementation details, not part of the control-plane contract.
type TunnelView struct {
	// ConfName is the bundled .conf basename used for the tunnel.
	ConfName string `json:"confName"`

	// ProxyURL is the internal cell SOCKS5 address, omitted when the tunnel is
	// still spawning. Clients receive a short-lived controller URL from POST
	// /v1/proxies instead.
	ProxyURL string `json:"proxyUrl,omitempty"`

	// State is the lifecycle phase.
	State TunnelState `json:"state"`

	// ExitCountry is configured metadata or the filename fallback.
	ExitCountry string `json:"exitCountry"`

	// ExitIP is reserved optional metadata and is normally empty.
	ExitIP string `json:"exitIP,omitempty"` //nolint:tagliatelle // API contract preserves the IP initialism

	// SpawnedAt is the docker-run timestamp.
	SpawnedAt time.Time `json:"spawnedAt"`

	// HealthyAt is the most recent wg-handshake timestamp.
	HealthyAt time.Time `json:"healthyAt,omitzero"`

	// LastUsedAt is the most recent proxy assignment timestamp.
	LastUsedAt time.Time `json:"lastUsedAt,omitzero"`

	// IdleSeconds is time.Since(LastUsedAt) at JSON-marshal time;
	// makes admin output instantly readable without client-side
	// math.
	IdleSeconds float64 `json:"idleSeconds"`
}

// ProxyResponse is the JSON returned by POST /v1/proxies.
type ProxyResponse struct {
	URL         string    `json:"url"`
	Pool        string    `json:"pool"`
	ExitCountry string    `json:"exitCountry"`
	ExitIP      string    `json:"exitIP,omitempty"` //nolint:tagliatelle // API contract preserves the IP initialism
	ExpiresAt   time.Time `json:"expiresAt"`
}

// ProxyView is one current tunnel in the authenticated active-proxy inventory.
// LastURL is the latest issued lease for that tunnel; it expires at
// LastURLExpiresAt and is omitted until a caller first allocates the tunnel.
type ProxyView struct {
	Pool             string      `json:"pool"`
	ConfName         string      `json:"confName"`
	State            TunnelState `json:"state"`
	ExitCountry      string      `json:"exitCountry"`
	ExitIP           string      `json:"exitIP,omitempty"` //nolint:tagliatelle // API contract preserves the IP initialism
	SpawnedAt        time.Time   `json:"spawnedAt"`
	HealthyAt        time.Time   `json:"healthyAt,omitzero"`
	LastUsedAt       time.Time   `json:"lastUsedAt,omitzero"`
	LastURL          string      `json:"lastURL,omitempty"`
	LastURLExpiresAt time.Time   `json:"lastURLExpiresAt,omitzero"`
	IdleSeconds      float64     `json:"idleSeconds"`
}

// ProxyListResponse is the bounded active-proxy collection returned by
// GET /v1/proxies.
type ProxyListResponse struct {
	Proxies []ProxyView `json:"proxies"`
	Limit   int         `json:"limit"`
	Offset  int         `json:"offset"`
	Total   int         `json:"total"`
}

// PoolListResponse is the bounded operator pool collection returned by
// GET /v1/pools.
type PoolListResponse struct {
	Pools  []PoolView `json:"pools"`
	Limit  int        `json:"limit"`
	Offset int        `json:"offset"`
	Total  int        `json:"total"`
}
