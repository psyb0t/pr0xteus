package pr0xteus

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/psyb0t/gonfiguration"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadAPIToken(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name       string
		contents   []byte
		want       string
		wantErrIs  error
		wantAnyErr bool
		create     bool
	}{
		{
			name:     "trims surrounding whitespace",
			contents: []byte(" \n test-token\t "),
			want:     "test-token",
			create:   true,
		},
		{
			name:       "empty",
			contents:   []byte(" \n\t "),
			wantErrIs:  ErrConfigInvalid,
			wantAnyErr: true,
			create:     true,
		},
		{name: "missing", wantAnyErr: true},
		{
			name:       "oversized",
			contents:   bytes.Repeat([]byte("x"), maxAPITokenBytes+1),
			wantErrIs:  ErrConfigInvalid,
			wantAnyErr: true,
			create:     true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			path := filepath.Join(t.TempDir(), "token")
			if tc.create {
				require.NoError(t, os.WriteFile(path, tc.contents, 0o600))
			}

			token, err := LoadAPIToken(path)
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
			name: "requires image digest outside local development",
			configure: func(t *testing.T) {
				t.Setenv("PR0XTEUS_CELL_IMAGE", "psyb0t/pr0xteus:cell-dev")
			},
			wantErr: ErrConfigInvalid,
		},
		{
			name: "allows explicit local unpinned image",
			configure: func(t *testing.T) {
				t.Setenv("PR0XTEUS_CELL_IMAGE", "psyb0t/pr0xteus:cell-dev")
				t.Setenv("PR0XTEUS_ALLOW_UNPINNED_CELL_IMAGE", "true")
			},
		},
		{
			name: "uses release-paired built cell image without an override",
			configure: func(t *testing.T) {
				t.Setenv("PR0XTEUS_CELL_IMAGE", "")
				t.Setenv("PR0XTEUS_BUILT_CELL_IMAGE", "psyb0t/pr0xteus:cell-v0.1.0")
			},
			wantCellImage: "psyb0t/pr0xteus:cell-v0.1.0",
		},
		{
			name: "requires an override or a release-paired image",
			configure: func(t *testing.T) {
				t.Setenv("PR0XTEUS_CELL_IMAGE", "")
				t.Setenv("PR0XTEUS_BUILT_CELL_IMAGE", "")
			},
			wantErr: ErrConfigInvalid,
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
			name: "requires token file path",
			configure: func(t *testing.T) {
				t.Setenv("PR0XTEUS_API_TOKEN_FILE", " ")
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

func configureValidEnvironment(t *testing.T) {
	t.Helper()
	gonfiguration.Reset()
	t.Cleanup(gonfiguration.Reset)

	t.Setenv(
		"PR0XTEUS_CELL_IMAGE",
		"psyb0t/pr0xteus:cell-v0.1.0@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	)
	t.Setenv("PR0XTEUS_BUILT_CELL_IMAGE", "")
	t.Setenv("PR0XTEUS_CELL_SOCKS_PORT", "1080")
	t.Setenv("PR0XTEUS_API_TOKEN_FILE", "/tmp/pr0xteus-test-token")
	t.Setenv("PR0XTEUS_ALLOW_UNPINNED_CELL_IMAGE", "false")
	t.Setenv("PR0XTEUS_MANAGED_SCOPE", "unit-test")
}
