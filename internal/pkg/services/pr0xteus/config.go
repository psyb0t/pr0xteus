package pr0xteus

import (
	"bytes"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/gonfiguration"
)

const (
	maxAPITokenBytes = 4096
	cellImageRepo    = "psyb0t/pr0xteus"
	defaultCellTag   = "dev"

	defaultSOCKSListenAddr = ":1080"
	defaultSOCKSPublicAddr = "127.0.0.1:1080"
	defaultProxyLeaseTTL   = 15 * time.Minute

	// minPort/maxPort bound every operator-supplied TCP port.
	minPort = 1
	maxPort = 65535
)

// configuredCellImage is set once by main before services initialize. The
// controller and cell therefore always use matching published build versions.
//
//nolint:gochecknoglobals // process startup config, set before services.Init.
var configuredCellImage = cellImageForVersion(defaultCellTag)

// configuredCellImageMu protects startup configuration for tests that load
// configuration concurrently. Production sets it once before services.Init.
//
//nolint:gochecknoglobals // process startup config, set before services.Init.
var configuredCellImageMu sync.RWMutex

// ConfigureCellImageVersion fixes the cell image to the version built into the
// controller binary. It deliberately has no environment equivalent: a
// controller release must never allocate a different cell release.
func ConfigureCellImageVersion(version string) {
	configuredCellImageMu.Lock()
	defer configuredCellImageMu.Unlock()

	configuredCellImage = cellImageForVersion(version)
}

func configuredCellImageValue() string {
	configuredCellImageMu.RLock()
	defer configuredCellImageMu.RUnlock()

	return configuredCellImage
}

func cellImageForVersion(version string) string {
	version = strings.TrimSpace(version)
	if version == "" {
		version = defaultCellTag
	}

	return cellImageRepo + ":cell-" + version
}

// Config is the env-driven configuration for pr0xteus. Each field is parsed
// once at boot; invalid configuration fails closed before Docker is touched.
type Config struct {
	// HTTPAddr is the authenticated control API listen address.
	HTTPAddr string `default:":8000" env:"TUNNEL_POOL_LISTEN_ADDR"`

	// MetricsAddr exposes unversioned /metrics and /healthz separately from the
	// authenticated control plane.
	MetricsAddr string `default:":9091" env:"TUNNEL_POOL_METRICS_ADDR"`

	// SOCKSAddr is the controller-fronted SOCKS5 listener. It selects an
	// allocated WireGuard cell by the short-lived credentials in the returned
	// proxy URL, so callers never need Docker network membership.
	SOCKSAddr string `default:":1080" env:"TUNNEL_POOL_SOCKS_ADDR"`

	// SOCKSPublicAddr is the address clients receive in allocated proxy URLs.
	// It must match the loopback, reverse-proxy, or tailnet address the operator
	// deliberately exposes for SOCKS5 traffic.
	SOCKSPublicAddr string `default:"127.0.0.1:1080" env:"TUNNEL_POOL_SOCKS_PUBLIC_ADDR"`

	// ProxyLeaseTTL limits how long an allocated SOCKS5 URL can be reused.
	ProxyLeaseTTL time.Duration `default:"15m" env:"TUNNEL_POOL_PROXY_LEASE_TTL"`

	// PoolsFile is the ignored, operator-managed pool definition file.
	PoolsFile string `default:"secrets/pools.yaml" env:"TUNNEL_POOL_POOLS_FILE"`

	// BundleDir holds the ignored WireGuard configuration bundle.
	BundleDir string `default:"secrets/wireguard" env:"TUNNEL_POOL_BUNDLE_DIR"`

	// RoutingFile maps country codes to logical pools.
	RoutingFile string `default:"config/egress-routing.yaml" env:"TUNNEL_POOL_ROUTING_FILE"` //nolint:lll // struct tag can't wrap

	// DefaultPool handles an unmapped country when configured.
	DefaultPool string `default:"neutral_clean" env:"TUNNEL_POOL_DEFAULT_POOL"`

	// IdleTimeout is how long a tunnel may stay unused before reaping.
	IdleTimeout time.Duration `default:"10m" env:"TUNNEL_POOL_IDLE_TIMEOUT"`

	// SpawnTimeout bounds a new cell's readiness wait.
	SpawnTimeout time.Duration `default:"20s" env:"TUNNEL_POOL_SPAWN_TIMEOUT"`

	// HealthCheckInterval controls tunnel handshake polling.
	HealthCheckInterval time.Duration `default:"30s" env:"TUNNEL_POOL_HEALTH_CHECK_INTERVAL"` //nolint:lll // struct tag can't wrap

	// HealthHandshakeMaxAge marks a stale WireGuard session unhealthy.
	HealthHandshakeMaxAge time.Duration `default:"180s" env:"TUNNEL_POOL_HEALTH_HANDSHAKE_MAX_AGE"` //nolint:lll // struct tag can't wrap

	// FailureCacheTTL avoids repeatedly selecting a recently failed config.
	FailureCacheTTL time.Duration `default:"5m" env:"TUNNEL_POOL_FAILURE_CACHE_TTL"`

	// CellImage identifies the release-paired WireGuard/SOCKS5 worker image.
	// LoadConfig assigns it from the controller binary's baked build version.
	CellImage string

	// CellSocksPort is the in-container SOCKS5 listener port.
	CellSocksPort int `default:"1080" env:"PR0XTEUS_CELL_SOCKS_PORT"`

	// CellControlPort is the in-container cellproxy control HTTP port, serving
	// /healthz and /status (traffic metrics). Reached by the controller over the
	// cell network only; never bound to the WireGuard egress side.
	CellControlPort int `default:"9090" env:"PR0XTEUS_CELL_CONTROL_PORT"`

	// ParentID is this controller's own container ID, stamped onto every cell it
	// spawns (both a docker label and an env var) so a cell records the parent it
	// belongs to and the controller can rediscover its children by label. Empty
	// falls back to the controller's hostname, which under docker is its own
	// short container ID.
	ParentID string `env:"PR0XTEUS_PARENT_ID"`

	// CellNetwork is the Docker network where cells and consumers meet. Empty
	// enables the deliberately limited host-loopback smoke-test mode.
	CellNetwork string `default:"" env:"PR0XTEUS_CELL_NETWORK"`

	// CellControlNetwork is an internal Docker network that carries only the
	// controller↔cell control-plane traffic (cellproxy /healthz + /status). When
	// set, cells join both CellNetwork (egress: WireGuard dial + SOCKS5 to
	// callers) and this network, and the controller reaches each cell's control
	// port over here instead of over the egress network — so the controller
	// itself needs no attachment to the egress network at all. Empty keeps the
	// single-network behavior (control port reached over CellNetwork).
	CellControlNetwork string `default:"" env:"PR0XTEUS_CELL_CONTROL_NETWORK"`

	// ManagedScope separates this controller's cells from another controller on
	// the same Docker daemon. It is especially important for isolated test runs.
	ManagedScope string `default:"default" env:"PR0XTEUS_MANAGED_SCOPE"`

	// DockerHost is the Docker API endpoint. Production Compose points this at
	// the restricted socket proxy, never the raw Docker socket.
	DockerHost string `default:"" env:"PR0XTEUS_DOCKER_HOST"`

	// APIToken protects the private control API. The standard deployment keeps
	// it in the ignored .env beside the Compose file.
	APIToken string `env:"PR0XTEUS_API_TOKEN,required"`
}

// LoadConfig parses and validates the environment.
func LoadConfig() (Config, error) {
	var cfg Config

	if err := gonfiguration.Parse(&cfg); err != nil {
		return Config{}, ctxerrors.Wrap(err, "parse pr0xteus env")
	}

	// The controller owns this pairing; an environment setting must not be able
	// to make a released controller run a mismatched cell.
	cfg.CellImage = configuredCellImageValue()

	if err := cfg.validatePorts(); err != nil {
		return Config{}, err
	}

	cfg.resolveParentID()

	if _, err := ValidateAPIToken(cfg.APIToken); err != nil {
		return Config{}, ctxerrors.Wrap(err, "validate PR0XTEUS_API_TOKEN")
	}

	if strings.TrimSpace(cfg.ManagedScope) == "" {
		return Config{}, ctxerrors.Wrap(
			ErrConfigInvalid, "PR0XTEUS_MANAGED_SCOPE required",
		)
	}

	return cfg, nil
}

// validatePorts checks the in-container cell ports are in range and distinct.
func (cfg *Config) validatePorts() error {
	if err := validateTCPAddress(cfg.SOCKSAddr, true, "TUNNEL_POOL_SOCKS_ADDR"); err != nil {
		return err
	}

	if err := validateTCPAddress(cfg.SOCKSPublicAddr, false, "TUNNEL_POOL_SOCKS_PUBLIC_ADDR"); err != nil {
		return err
	}

	if cfg.ProxyLeaseTTL <= 0 {
		return ctxerrors.Wrap(
			ErrConfigInvalid, "TUNNEL_POOL_PROXY_LEASE_TTL must be positive",
		)
	}

	if cfg.CellSocksPort < minPort || cfg.CellSocksPort > maxPort {
		return ctxerrors.Wrap(
			ErrConfigInvalid, "PR0XTEUS_CELL_SOCKS_PORT must be in 1..65535",
		)
	}

	if cfg.CellControlPort < minPort || cfg.CellControlPort > maxPort {
		return ctxerrors.Wrap(
			ErrConfigInvalid, "PR0XTEUS_CELL_CONTROL_PORT must be in 1..65535",
		)
	}

	if cfg.CellControlPort == cfg.CellSocksPort {
		return ctxerrors.Wrap(
			ErrConfigInvalid,
			"PR0XTEUS_CELL_CONTROL_PORT must differ from PR0XTEUS_CELL_SOCKS_PORT",
		)
	}

	return nil
}

func validateTCPAddress(address string, allowEmptyHost bool, envName string) error {
	host, rawPort, err := net.SplitHostPort(address)
	if err != nil {
		return ctxerrors.Wrapf(ErrConfigInvalid, "%s must be host:port", envName)
	}

	if !allowEmptyHost && strings.TrimSpace(host) == "" {
		return ctxerrors.Wrapf(ErrConfigInvalid, "%s host is required", envName)
	}

	port, err := strconv.Atoi(rawPort)
	if err != nil || port < minPort || port > maxPort {
		return ctxerrors.Wrapf(ErrConfigInvalid, "%s port must be in 1..65535", envName)
	}

	return nil
}

func (cfg Config) socksPublicAddr() string {
	if cfg.SOCKSPublicAddr != "" {
		return cfg.SOCKSPublicAddr
	}

	return defaultSOCKSPublicAddr
}

func (cfg Config) socksListenAddr() string {
	if cfg.SOCKSAddr != "" {
		return cfg.SOCKSAddr
	}

	return defaultSOCKSListenAddr
}

func (cfg Config) proxyLeaseTTL() time.Duration {
	if cfg.ProxyLeaseTTL > 0 {
		return cfg.ProxyLeaseTTL
	}

	return defaultProxyLeaseTTL
}

// resolveParentID fills ParentID from the controller's hostname when the
// operator did not set PR0XTEUS_PARENT_ID. Under docker the hostname is the
// container's own short ID, which is exactly the parent identity a cell records.
// A hostname lookup failure is non-fatal: cells simply carry no parent label.
func (cfg *Config) resolveParentID() {
	if strings.TrimSpace(cfg.ParentID) != "" {
		return
	}

	hostname, err := os.Hostname()
	if err != nil {
		return
	}

	cfg.ParentID = strings.TrimSpace(hostname)
}

// ValidateAPIToken validates the private bearer token without logging it.
func ValidateAPIToken(raw string) ([]byte, error) {
	token := bytes.Clone(bytes.TrimSpace([]byte(raw)))
	if len(token) > maxAPITokenBytes {
		return nil, ctxerrors.Wrap(
			ErrConfigInvalid, "API token exceeds 4096 bytes",
		)
	}

	if len(token) == 0 {
		return nil, ctxerrors.Wrap(ErrConfigInvalid, "API token is empty")
	}

	return token, nil
}
