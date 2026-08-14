//go:build integration

package testinfra

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWithExternalProvider_ValidatesFilesBeforeFixtureStartup(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	bundleDir := filepath.Join(tempDir, "bundle")
	require.NoError(t, os.Mkdir(bundleDir, workDirectoryMode))
	require.NoError(t, os.WriteFile(
		filepath.Join(bundleDir, "test.conf"), []byte("[Interface]\n"), secretFileMode,
	))

	poolsFile := filepath.Join(tempDir, "pools.yaml")
	require.NoError(t, os.WriteFile(poolsFile, []byte("pools: {}\n"), secretFileMode))
	routingFile := filepath.Join(tempDir, "routing.yaml")
	require.NoError(t, os.WriteFile(routingFile, []byte("country_to_pool: {}\n"), secretFileMode))

	options := setupOptions{}
	require.NoError(t, WithExternalProvider(ExternalProviderConfig{
		BundleDir:   bundleDir,
		PoolsFile:   poolsFile,
		RoutingFile: routingFile,
	})(&options))
	require.NotNil(t, options.externalProvider)

	testCases := []struct {
		name    string
		config  ExternalProviderConfig
		prepare func(t *testing.T)
	}{
		{
			name: "missing bundle",
			config: ExternalProviderConfig{
				BundleDir:   filepath.Join(tempDir, "missing"),
				PoolsFile:   poolsFile,
				RoutingFile: routingFile,
			},
		},
		{
			name: "unreadable bundle",
			config: ExternalProviderConfig{
				BundleDir:   bundleDir,
				PoolsFile:   poolsFile,
				RoutingFile: routingFile,
			},
			prepare: func(t *testing.T) {
				t.Helper()
				require.NoError(t, os.Chmod(bundleDir, 0o000))
				t.Cleanup(func() {
					require.NoError(t, os.Chmod(bundleDir, workDirectoryParentMode))
				})
			},
		},
		{
			name: "missing pools file",
			config: ExternalProviderConfig{
				BundleDir:   bundleDir,
				PoolsFile:   filepath.Join(tempDir, "missing-pools.yaml"),
				RoutingFile: routingFile,
			},
		},
		{
			name: "unreadable pools file",
			config: ExternalProviderConfig{
				BundleDir:   bundleDir,
				PoolsFile:   poolsFile,
				RoutingFile: routingFile,
			},
			prepare: func(t *testing.T) {
				t.Helper()
				require.NoError(t, os.Chmod(poolsFile, 0o000))
				t.Cleanup(func() {
					require.NoError(t, os.Chmod(poolsFile, secretFileMode))
				})
			},
		},
		{
			name: "missing routing file",
			config: ExternalProviderConfig{
				BundleDir:   bundleDir,
				PoolsFile:   poolsFile,
				RoutingFile: filepath.Join(tempDir, "missing-routing.yaml"),
			},
		},
		{
			name: "unreadable routing file",
			config: ExternalProviderConfig{
				BundleDir:   bundleDir,
				PoolsFile:   poolsFile,
				RoutingFile: routingFile,
			},
			prepare: func(t *testing.T) {
				t.Helper()
				require.NoError(t, os.Chmod(routingFile, 0o000))
				t.Cleanup(func() {
					require.NoError(t, os.Chmod(routingFile, secretFileMode))
				})
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.prepare != nil {
				tc.prepare(t)
			}

			missingOptions := setupOptions{}
			require.Error(t, WithExternalProvider(tc.config)(&missingOptions))
			require.Nil(t, missingOptions.externalProvider)
		})
	}
}
