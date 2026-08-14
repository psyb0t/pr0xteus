package pr0xteus

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadConfig_ControlPortValidation(t *testing.T) {
	// t.Setenv changes process state and cannot run in parallel.
	testCases := []struct {
		name      string
		configure func(t *testing.T)
		wantErr   error
	}{
		{
			name: "defaults to 9090",
			configure: func(_ *testing.T) {
			},
		},
		{
			name: "rejects control port below range",
			configure: func(t *testing.T) {
				t.Setenv("PR0XTEUS_CELL_CONTROL_PORT", "0")
			},
			wantErr: ErrConfigInvalid,
		},
		{
			name: "rejects control port above range",
			configure: func(t *testing.T) {
				t.Setenv("PR0XTEUS_CELL_CONTROL_PORT", "70000")
			},
			wantErr: ErrConfigInvalid,
		},
		{
			name: "rejects control port equal to socks port",
			configure: func(t *testing.T) {
				t.Setenv("PR0XTEUS_CELL_SOCKS_PORT", "1080")
				t.Setenv("PR0XTEUS_CELL_CONTROL_PORT", "1080")
			},
			wantErr: ErrConfigInvalid,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			configureValidEnvironment(t)
			tc.configure(t)

			cfg, err := LoadConfig()
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, 9090, cfg.CellControlPort)
		})
	}
}

func TestConfig_ResolveParentID(t *testing.T) {
	t.Parallel()

	t.Run("keeps an explicit parent id", func(t *testing.T) {
		t.Parallel()

		cfg := Config{ParentID: "explicit-parent"}
		cfg.resolveParentID()
		assert.Equal(t, "explicit-parent", cfg.ParentID)
	})

	t.Run("falls back to hostname", func(t *testing.T) {
		t.Parallel()

		cfg := Config{}
		cfg.resolveParentID()
		assert.NotEmpty(t, cfg.ParentID)
	})
}
