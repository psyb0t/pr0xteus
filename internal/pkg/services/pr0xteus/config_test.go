package pr0xteus

import (
	"strings"
	"testing"

	"github.com/psyb0t/gonfiguration"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateAPIToken(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name       string
		raw        string
		want       string
		wantErrIs  error
		wantAnyErr bool
	}{
		{
			name: "trims surrounding whitespace",
			raw:  " \n test-token\t ",
			want: "test-token",
		},
		{
			name:       "empty",
			raw:        " \n\t ",
			wantErrIs:  ErrConfigInvalid,
			wantAnyErr: true,
		},
		{
			name:       "oversized",
			raw:        strings.Repeat("x", maxAPITokenBytes+1),
			wantErrIs:  ErrConfigInvalid,
			wantAnyErr: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			token, err := ValidateAPIToken(tc.raw)
			if tc.wantAnyErr {
				require.Error(t, err)
				if tc.wantErrIs != nil {
					require.ErrorIs(t, err, tc.wantErrIs)
				}
				assert.Nil(t, token)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, tc.want, string(token))
		})
	}
}

func TestLoadConfig_Validation(t *testing.T) {
	// t.Setenv changes process state and cannot run in parallel.
	testCases := []struct {
		name          string
		configure     func(t *testing.T)
		wantCellImage string
		wantErr       error
	}{
		{
			name:          "uses the development cell by default",
			wantCellImage: "psyb0t/pr0xteus:cell-dev",
		},
		{
			name: "ignores an environment cell image override",
			configure: func(t *testing.T) {
				t.Setenv("PR0XTEUS_CELL_IMAGE", "other/image:wrong")
			},
			wantCellImage: "psyb0t/pr0xteus:cell-dev",
		},
		{
			name: "rejects port below range",
			configure: func(t *testing.T) {
				t.Setenv("PR0XTEUS_CELL_SOCKS_PORT", "0")
			},
			wantErr: ErrConfigInvalid,
		},
		{
			name: "rejects port above range",
			configure: func(t *testing.T) {
				t.Setenv("PR0XTEUS_CELL_SOCKS_PORT", "65536")
			},
			wantErr: ErrConfigInvalid,
		},
		{
			name: "rejects malformed SOCKS listener",
			configure: func(t *testing.T) {
				t.Setenv("TUNNEL_POOL_SOCKS_ADDR", "1080")
			},
			wantErr: ErrConfigInvalid,
		},
		{
			name: "rejects SOCKS public address without host",
			configure: func(t *testing.T) {
				t.Setenv("TUNNEL_POOL_SOCKS_PUBLIC_ADDR", ":1080")
			},
			wantErr: ErrConfigInvalid,
		},
		{
			name: "rejects nonpositive proxy lease TTL",
			configure: func(t *testing.T) {
				t.Setenv("TUNNEL_POOL_PROXY_LEASE_TTL", "0s")
			},
			wantErr: ErrConfigInvalid,
		},
		{
			name: "requires API token",
			configure: func(t *testing.T) {
				t.Setenv("PR0XTEUS_API_TOKEN", " ")
			},
			wantErr: ErrConfigInvalid,
		},
		{
			name: "requires a managed scope",
			configure: func(t *testing.T) {
				t.Setenv("PR0XTEUS_MANAGED_SCOPE", " ")
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
			if tc.wantCellImage != "" {
				assert.Equal(t, tc.wantCellImage, cfg.CellImage)
			}
		})
	}
}

func TestConfigureCellImageVersion(t *testing.T) {
	ConfigureCellImageVersion("v1.2.3")
	t.Cleanup(func() {
		ConfigureCellImageVersion(defaultCellTag)
	})

	assert.Equal(t, "psyb0t/pr0xteus:cell-v1.2.3", configuredCellImageValue())
	assert.Equal(t, "psyb0t/pr0xteus:cell-dev", cellImageForVersion("  "))

	configureValidEnvironment(t)
	cfg, err := LoadConfig()
	require.NoError(t, err)
	assert.Equal(t, "psyb0t/pr0xteus:cell-v1.2.3", cfg.CellImage)
}

func configureValidEnvironment(t *testing.T) {
	t.Helper()
	gonfiguration.Reset()
	t.Cleanup(gonfiguration.Reset)

	t.Setenv("PR0XTEUS_CELL_SOCKS_PORT", "1080")
	t.Setenv("PR0XTEUS_API_TOKEN", "test-only-token")
	t.Setenv("PR0XTEUS_MANAGED_SCOPE", "unit-test")
}
