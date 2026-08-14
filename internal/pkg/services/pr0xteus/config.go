package pr0xteus

import (
	"bytes"
	"os"
	"strings"
	"time"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/gonfiguration"
)

const (
	maxAPITokenBytes = 4096

	// minPort/maxPort bound every operator-supplied TCP port.
	minPort = 1
	maxPort = 65535
)

// Config is the env-driven configuration for pr0xteus. Each field is parsed
// once at boot; invalid configuration fails closed before Docker is touched.
type Config struct {
	// HTTPAddr is the authenticated control API listen address.
	HTTPAddr string `default:":8000" env:"TUNNEL_POOL_LISTEN_ADDR"`

	// MetricsAddr exposes unversioned /metrics and /healthz separately from the
	// authenticated control plane.
	MetricsAddr string `default:":9091" env:"TUNNEL_POOL_METRICS_ADDR"`

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

	// CellImage identifies the WireGuard/SOCKS5 worker image.
	CellImage string `default:"" env:"PR0XTEUS_CELL_IMAGE"`

	// BuiltCellImage is the release-paired cell image baked into a controller
	// image. PR0XTEUS_CELL_IMAGE remains the operator override.
	BuiltCellImage string `default:"" env:"PR0XTEUS_BUILT_CELL_IMAGE"`

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

	// ManagedScope separates this controller's cells from another controller on
	// the same Docker daemon. It is especially important for isolated test runs.
	ManagedScope string `default:"default" env:"PR0XTEUS_MANAGED_SCOPE"`

	// DockerHost is the Docker API endpoint. Production Compose points this at
	// the restricted socket proxy, never the raw Docker socket.
	DockerHost string `default:"" env:"PR0XTEUS_DOCKER_HOST"`

	// APIToken protects the private control API. The standard deployment keeps
	// it in the ignored .env beside the Compose file.
	APIToken string `env:"PR0XTEUS_API_TOKEN,required"`

	// AllowUnpinnedCellImage is a local-development escape hatch. Production
	// must use an immutable image digest.
	AllowUnpinnedCellImage bool `default:"false" env:"PR0XTEUS_ALLOW_UNPINNED_CELL_IMAGE"` //nolint:lll // struct tag can't wrap
}

// LoadConfig parses and validates the environment.
func LoadConfig() (Config, error) {
	var cfg Config

	if err := gonfiguration.Parse(&cfg); err != nil {
		return Config{}, ctxerrors.Wrap(err, "parse pr0xteus env")
	}

	cellImageOverridden, err := cfg.resolveCellImage()
	if err != nil {
		return Config{}, err
	}

	if cellImageOverridden && !cfg.AllowUnpinnedCellImage && !hasImageDigest(cfg.CellImage) {
		return Config{}, ctxerrors.Wrap(
			ErrConfigInvalid,
			"PR0XTEUS_CELL_IMAGE override must be pinned by @sha256:<digest>",
		)
	}

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

func (cfg *Config) resolveCellImage() (bool, error) {
	cellImageOverridden := strings.TrimSpace(cfg.CellImage) != ""
	if cellImageOverridden {
		return true, nil
	}

	cfg.CellImage = strings.TrimSpace(cfg.BuiltCellImage)
	if cfg.CellImage != "" {
		return false, nil
	}

	return false, ctxerrors.Wrap(
		ErrConfigInvalid,
		"PR0XTEUS_CELL_IMAGE or PR0XTEUS_BUILT_CELL_IMAGE required",
	)
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

// hasImageDigest reports whether image includes an immutable SHA-256 digest.
func hasImageDigest(image string) bool {
	return strings.Contains(image, "@sha256:")
}
