package cellproxy

import (
	"net"
	"os"
	"strconv"
	"time"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/gonfiguration"
)

const (
	networkTCP  = "tcp"
	minPort     = 1
	maxPort     = 65535
	defaultBind = "0.0.0.0"
)

// envConfig is the environment-driven configuration for the cell proxy. The
// SOCKS5 bind/port keys match what the cell entrypoint already sets for
// microsocks; the control keys are new.
type envConfig struct {
	// ParentID is the spawning controller's own container ID. The controller
	// passes it so each cell knows which parent it belongs to; the cell echoes
	// it in /stats and tags its logs with it.
	ParentID        string        `default:""        env:"PR0XTEUS_PARENT_ID"`
	SOCKSBind       string        `default:"0.0.0.0" env:"PR0XTEUS_SOCKS5_BIND"`
	SOCKSPort       int           `default:"1080"    env:"PR0XTEUS_SOCKS5_PORT"`
	ControlBind     string        `default:"0.0.0.0" env:"PR0XTEUS_CELL_CONTROL_BIND"`
	ControlPort     int           `default:"9090"    env:"PR0XTEUS_CELL_CONTROL_PORT"`
	DialTimeout     time.Duration `default:"30s"     env:"PR0XTEUS_CELL_DIAL_TIMEOUT"`
	TopDestinations int           `default:"50"      env:"PR0XTEUS_CELL_TOP_DESTINATIONS"` //nolint:lll // struct tag can't wrap
}

// LoadConfig parses and validates the cell proxy environment.
func LoadConfig() (Config, error) {
	var env envConfig

	if err := gonfiguration.Parse(&env); err != nil {
		return Config{}, ctxerrors.Wrap(err, "parse cellproxy env")
	}

	if err := validatePort(env.SOCKSPort, "PR0XTEUS_SOCKS5_PORT"); err != nil {
		return Config{}, err
	}

	if err := validatePort(env.ControlPort, "PR0XTEUS_CELL_CONTROL_PORT"); err != nil {
		return Config{}, err
	}

	// A cell's own identity is its short container ID, which Docker sets as the
	// hostname. An unreadable hostname is not fatal — it only weakens log/stats
	// correlation.
	cellID, err := os.Hostname()
	if err != nil {
		cellID = ""
	}

	return Config{
		CellID:          cellID,
		ParentID:        env.ParentID,
		SOCKSNetwork:    networkTCP,
		SOCKSAddr:       net.JoinHostPort(env.SOCKSBind, strconv.Itoa(env.SOCKSPort)),
		ControlAddr:     net.JoinHostPort(env.ControlBind, strconv.Itoa(env.ControlPort)),
		DialTimeout:     env.DialTimeout,
		TopDestinations: env.TopDestinations,
	}, nil
}

func validatePort(port int, name string) error {
	if port < minPort || port > maxPort {
		return ctxerrors.Wrapf(commerr.ErrValidationFailed, "%s must be in %d..%d", name, minPort, maxPort)
	}

	return nil
}
