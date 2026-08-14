package pr0xteus

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBootstrapConfigCreatesPrivateOperatorFilesAndPreservesThem(t *testing.T) {
	t.Parallel()

	configDir := t.TempDir()
	result, err := BootstrapConfig(BootstrapOptions{
		ConfigDir:       configDir,
		HostConfigDir:   configDir,
		ControllerImage: defaultControllerImage,
	})
	require.NoError(t, err)
	assert.Len(t, result.Created, 3)
	assert.Empty(t, result.Preserved)

	envPath := filepath.Join(configDir, dotEnvFileName)
	env, err := os.ReadFile(envPath)
	require.NoError(t, err)
	assert.Contains(t, string(env), apiTokenEnvName+"=")
	assert.Contains(t, string(env), "PR0XTEUS_CONTROLLER_IMAGE="+defaultControllerImage)
	assert.NotContains(t, string(env), "API_TOKEN_FILE")

	info, err := os.Stat(envPath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(bootstrapFileMode), info.Mode().Perm())

	poolPath := filepath.Join(configDir, poolsRelativePath)
	routingPath := filepath.Join(configDir, routingRelativePath)
	poolBefore, err := os.ReadFile(poolPath)
	require.NoError(t, err)
	routingBefore, err := os.ReadFile(routingPath)
	require.NoError(t, err)

	secondResult, err := BootstrapConfig(BootstrapOptions{
		ConfigDir:       configDir,
		HostConfigDir:   configDir,
		ControllerImage: defaultControllerImage,
	})
	require.NoError(t, err)
	assert.Empty(t, secondResult.Created)
	assert.Len(t, secondResult.Preserved, 3)
	assert.Equal(t, poolBefore, readBootstrapFixture(t, poolPath))
	assert.Equal(t, routingBefore, readBootstrapFixture(t, routingPath))
	assert.Equal(t, env, readBootstrapFixture(t, envPath))
}

func TestCheckConfigValidatesGeneratedTokenAndOperatorFiles(t *testing.T) {
	t.Parallel()

	configDir := t.TempDir()
	_, err := BootstrapConfig(BootstrapOptions{
		ConfigDir:       configDir,
		HostConfigDir:   configDir,
		ControllerImage: defaultControllerImage,
	})
	require.NoError(t, err)

	bundleDir := filepath.Join(configDir, bundleRelativePath)
	require.NoError(t, os.WriteFile(filepath.Join(bundleDir, "test.conf"), []byte("[Interface]\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(configDir, poolsRelativePath), []byte(`pools:
  test:
    region: eu
    purpose: test
    configs: [test]
    exit_countries:
      test: DE
`), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(configDir, routingRelativePath), []byte(`country_to_pool:
  DE: test
default_pool: test
`), 0o600))

	require.NoError(t, CheckConfig(ConfigCheckOptions{ConfigDir: configDir}))
}

func TestBootstrapConfigRejectsUnsafeConfigDirectory(t *testing.T) {
	t.Parallel()

	t.Run("relative path", func(t *testing.T) {
		t.Parallel()

		_, err := BootstrapConfig(BootstrapOptions{
			ConfigDir:       "relative",
			HostConfigDir:   t.TempDir(),
			ControllerImage: defaultControllerImage,
		})
		require.ErrorIs(t, err, ErrConfigInvalid)
	})

	t.Run("symlink", func(t *testing.T) {
		t.Parallel()

		target := t.TempDir()
		symlinkPath := filepath.Join(t.TempDir(), "config")
		require.NoError(t, os.Symlink(target, symlinkPath))

		_, err := BootstrapConfig(BootstrapOptions{
			ConfigDir:       symlinkPath,
			HostConfigDir:   target,
			ControllerImage: defaultControllerImage,
		})
		require.ErrorIs(t, err, ErrConfigInvalid)
	})
}

func TestCheckConfigRejectsMissingOrOversizedToken(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name  string
		token string
	}{
		{name: "missing", token: ""},
		{name: "oversized", token: string(make([]byte, maxAPITokenBytes+1))},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			configDir := t.TempDir()
			require.NoError(t, os.WriteFile(
				filepath.Join(configDir, dotEnvFileName),
				[]byte(apiTokenEnvName+"="+tc.token+"\n"),
				0o600,
			))

			err := CheckConfig(ConfigCheckOptions{ConfigDir: configDir})
			require.ErrorIs(t, err, ErrConfigInvalid)
		})
	}
}

func readBootstrapFixture(t *testing.T, path string) []byte {
	t.Helper()

	contents, err := os.ReadFile(path)
	require.NoError(t, err)

	return contents
}
