package pr0xteus

import (
	"strings"
	"testing"
	"time"

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
			name: "rejects malformed HTTP proxy listener",
			configure: func(t *testing.T) {
				t.Setenv("TUNNEL_POOL_HTTP_PROXY_ADDR", "8080")
			},
			wantErr: ErrConfigInvalid,
		},
		{
			name: "rejects HTTP proxy public address without host",
			configure: func(t *testing.T) {
				t.Setenv("TUNNEL_POOL_HTTP_PROXY_PUBLIC_ADDR", ":8080")
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
			if tc.configure != nil {
				tc.configure(t)
			}

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

func TestLoadConfig_DefaultProxyAddresses(t *testing.T) {
	configureValidEnvironment(t)

	cfg, err := LoadConfig()
	require.NoError(t, err)
	assert.Equal(t, defaultSOCKSListenAddr, cfg.socksListenAddr())
	assert.Equal(t, defaultSOCKSPublicAddr, cfg.socksPublicAddr())
	assert.Equal(t, defaultHTTPProxyListenAddr, cfg.httpProxyListenAddr())
	assert.Equal(t, defaultHTTPProxyPublicAddr, cfg.httpProxyPublicAddr())
}

func TestConfig_UsesProxyDefaultsForAnEmptyConfig(t *testing.T) {
	t.Parallel()

	cfg := Config{}
	assert.Equal(t, defaultSOCKSListenAddr, cfg.socksListenAddr())
	assert.Equal(t, defaultSOCKSPublicAddr, cfg.socksPublicAddr())
	assert.Equal(t, defaultHTTPProxyListenAddr, cfg.httpProxyListenAddr())
	assert.Equal(t, defaultHTTPProxyPublicAddr, cfg.httpProxyPublicAddr())
	assert.Equal(t, defaultProxyLeaseTTL, cfg.proxyLeaseTTL())

	cfg.ProxyLeaseTTL = time.Minute
	assert.Equal(t, time.Minute, cfg.proxyLeaseTTL())
}

func TestValidateTCPAddress(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name           string
		address        string
		allowEmptyHost bool
		wantErr        bool
	}{
		{name: "listener accepts an empty host", address: ":8080", allowEmptyHost: true},
		{name: "public address requires a host", address: ":8080", wantErr: true},
		{name: "accepts an IPv4 host", address: "127.0.0.1:8080"},
		{name: "rejects malformed address", address: "8080", wantErr: true},
		{name: "rejects nonnumeric port", address: "proxy.example:abc", wantErr: true},
		{name: "rejects out of range port", address: "proxy.example:65536", wantErr: true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := validateTCPAddress(tc.address, tc.allowEmptyHost, "TEST_ADDRESS")
			if tc.wantErr {
				require.ErrorIs(t, err, ErrConfigInvalid)

				return
			}

			require.NoError(t, err)
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
