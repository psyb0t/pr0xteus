package pr0xteus

import (
	"bytes"
	"os"
	"strings"
	"time"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/gonfiguration"
)

const maxAPITokenBytes = 4096

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

	// CellNetwork is the Docker network where cells and consumers meet. Empty
	// enables the deliberately limited host-loopback smoke-test mode.
	CellNetwork string `default:"" env:"PR0XTEUS_CELL_NETWORK"`

	// ManagedScope separates this controller's cells from another controller on
	// the same Docker daemon. It is especially important for isolated test runs.
	ManagedScope string `default:"default" env:"PR0XTEUS_MANAGED_SCOPE"`

	// DockerHost is the Docker API endpoint. Production Compose points this at
	// the restricted socket proxy, never the raw Docker socket.
	DockerHost string `default:"" env:"PR0XTEUS_DOCKER_HOST"`

	// APITokenFile contains the private bearer token that protects the control
	// API. The standard Compose setup mounts it as a Docker secret.
	APITokenFile string `default:"/run/secrets/pr0xteus_api_token" env:"PR0XTEUS_API_TOKEN_FILE"` //nolint:lll // struct tag can't wrap

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

	if cfg.CellSocksPort < 1 || cfg.CellSocksPort > 65535 {
		return Config{}, ctxerrors.Wrap(
			ErrConfigInvalid, "PR0XTEUS_CELL_SOCKS_PORT must be in 1..65535",
		)
	}

	if strings.TrimSpace(cfg.APITokenFile) == "" {
		return Config{}, ctxerrors.Wrap(
			ErrConfigInvalid, "PR0XTEUS_API_TOKEN_FILE required",
		)
	}

	if strings.TrimSpace(cfg.ManagedScope) == "" {
		return Config{}, ctxerrors.Wrap(
			ErrConfigInvalid, "PR0XTEUS_MANAGED_SCOPE required",
		)
	}

	return cfg, nil
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

// LoadAPIToken reads the mounted bearer token without ever logging its value.
func LoadAPIToken(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "stat API token file")
	}

	if info.Size() > maxAPITokenBytes {
		return nil, ctxerrors.Wrap(
			ErrConfigInvalid, "API token file exceeds 4096 bytes",
		)
	}

	token, err := os.ReadFile(path)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "read API token file")
	}

	token = bytes.Clone(bytes.TrimSpace(token))
	if len(token) == 0 {
		return nil, ctxerrors.Wrap(ErrConfigInvalid, "API token file is empty")
	}

	return token, nil
}

// hasImageDigest reports whether image includes an immutable SHA-256 digest.
func hasImageDigest(image string) bool {
	return strings.Contains(image, "@sha256:")
}
