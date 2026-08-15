package cellproxy

import (
	"testing"
	"time"

	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/gonfiguration"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidatePort(t *testing.T) {
	t.Parallel()

	require.NoError(t, validatePort(minPort, "P"))
	require.NoError(t, validatePort(maxPort, "P"))
	require.ErrorIs(t, validatePort(0, "P"), commerr.ErrValidationFailed)
	require.ErrorIs(t, validatePort(maxPort+1, "P"), commerr.ErrValidationFailed)
}

func TestLoadConfig_Defaults(t *testing.T) {
	gonfiguration.Reset()
	t.Cleanup(gonfiguration.Reset)

	cfg, err := LoadConfig()
	require.NoError(t, err)
	assert.Equal(t, networkTCP, cfg.SOCKSNetwork)
	assert.Equal(t, "0.0.0.0:1080", cfg.SOCKSAddr)
	assert.Equal(t, "0.0.0.0:9090", cfg.ControlAddr)
	assert.Equal(t, 30*time.Second, cfg.DialTimeout)
	assert.Equal(t, 50, cfg.TopDestinations)
}

func TestLoadConfig_CustomEnv(t *testing.T) {
	gonfiguration.Reset()
	t.Cleanup(gonfiguration.Reset)
	t.Setenv("PR0XTEUS_PARENT_ID", "ctrl-xyz")
	t.Setenv("PR0XTEUS_SOCKS5_PORT", "1081")
	t.Setenv("PR0XTEUS_CELL_CONTROL_PORT", "9091")
	t.Setenv("PR0XTEUS_CELL_TOP_DESTINATIONS", "10")

	cfg, err := LoadConfig()
	require.NoError(t, err)
	assert.Equal(t, "ctrl-xyz", cfg.ParentID)
	assert.Equal(t, "0.0.0.0:1081", cfg.SOCKSAddr)
	assert.Equal(t, "0.0.0.0:9091", cfg.ControlAddr)
	assert.Equal(t, 10, cfg.TopDestinations)
}

func TestLoadConfig_RejectsInvalidPorts(t *testing.T) {
	testCases := []struct {
		name  string
		env   string
		value string
	}{
		{name: "socks port out of range", env: "PR0XTEUS_SOCKS5_PORT", value: "0"},
		{name: "control port out of range", env: "PR0XTEUS_CELL_CONTROL_PORT", value: "70000"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			gonfiguration.Reset()
			t.Cleanup(gonfiguration.Reset)
			t.Setenv(tc.env, tc.value)

			_, err := LoadConfig()
			require.ErrorIs(t, err, commerr.ErrValidationFailed)
		})
	}
}
