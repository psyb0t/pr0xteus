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
	assert.Len(t, result.Created, 6)
	assert.Empty(t, result.Preserved)
	assert.Len(t, result.Refreshed, 1)

	envPath := filepath.Join(configDir, dotEnvFileName)
	env, err := os.ReadFile(envPath)
	require.NoError(t, err)
	assert.Contains(t, string(env), apiTokenEnvName+"=")
	assert.Contains(t, string(env), "PR0XTEUS_CONTROLLER_IMAGE="+defaultControllerImage)
	assert.Contains(t, string(env), "PR0XTEUS_TAILSCALE_ENABLED=false")
	assert.NotContains(t, string(env), "API_TOKEN_FILE")
	assert.FileExists(t, filepath.Join(configDir, dotEnvExampleFileName))

	info, err := os.Stat(envPath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(bootstrapFileMode), info.Mode().Perm())

	poolPath := filepath.Join(configDir, poolsRelativePath)
	routingPath := filepath.Join(configDir, routingRelativePath)
	composePath := filepath.Join(configDir, composeFileName)
	hostPortsPath := filepath.Join(configDir, hostPortsFileName)
	noHostPortsPath := filepath.Join(configDir, noHostPortsFileName)
	poolBefore, err := os.ReadFile(poolPath)
	require.NoError(t, err)
	routingBefore, err := os.ReadFile(routingPath)
	require.NoError(t, err)
	composeBefore, err := os.ReadFile(composePath)
	require.NoError(t, err)
	hostPortsBefore, err := os.ReadFile(hostPortsPath)
	require.NoError(t, err)
	noHostPortsBefore, err := os.ReadFile(noHostPortsPath)
	require.NoError(t, err)
	assert.Contains(t, string(composeBefore), "name: pr0xteus")
	assert.Contains(t, string(composeBefore), "docker-socket-proxy")
	assert.Contains(t, string(composeBefore), "tailscale/tailscale")
	assert.Contains(t, string(hostPortsBefore), "PR0XTEUS_HTTP_HOST_PORT")
	assert.Contains(t, string(noHostPortsBefore), "ports: !reset []")
	require.DirExists(t, filepath.Join(configDir, tailscaleStatePath))

	secondResult, err := BootstrapConfig(BootstrapOptions{
		ConfigDir:       configDir,
		HostConfigDir:   configDir,
		ControllerImage: defaultControllerImage,
	})
	require.NoError(t, err)
	assert.Empty(t, secondResult.Created)
	assert.Len(t, secondResult.Preserved, 6)
	assert.Len(t, secondResult.Refreshed, 1)
	assert.Equal(t, poolBefore, readBootstrapFixture(t, poolPath))
	assert.Equal(t, routingBefore, readBootstrapFixture(t, routingPath))
	assert.Equal(t, composeBefore, readBootstrapFixture(t, composePath))
	assert.Equal(t, hostPortsBefore, readBootstrapFixture(t, hostPortsPath))
	assert.Equal(t, noHostPortsBefore, readBootstrapFixture(t, noHostPortsPath))
	assert.Equal(t, env, readBootstrapFixture(t, envPath))
}

func TestBootstrapConfigRefreshesExampleAndRuntimeUserWithoutReplacingDotEnv(t *testing.T) {
	t.Parallel()

	configDir := t.TempDir()
	_, err := BootstrapConfig(BootstrapOptions{
		ConfigDir:       configDir,
		HostConfigDir:   configDir,
		ControllerImage: defaultControllerImage,
	})
	require.NoError(t, err)

	envPath := filepath.Join(configDir, dotEnvFileName)
	examplePath := filepath.Join(configDir, dotEnvExampleFileName)
	require.NoError(
		t,
		os.WriteFile(
			envPath,
			[]byte("# keep this comment\nPR0XTEUS_API_TOKEN=keep-me\nCUSTOM_VALUE=keep-me\n"),
			bootstrapFileMode,
		),
	)
	require.NoError(t, os.WriteFile(examplePath, []byte("stale\n"), bootstrapExampleMode))

	result, err := BootstrapConfig(BootstrapOptions{
		ConfigDir:       configDir,
		HostConfigDir:   configDir,
		ControllerImage: "psyb0t/pr0xteus:v9.9.9",
	})
	require.NoError(t, err)
	runtimeUser, err := checkedRuntimeUser("")
	require.NoError(t, err)
	assert.Equal(t, []string{filepath.Join(configDir, dotEnvExampleFileName)}, result.Refreshed)
	assert.Equal(
		t,
		"# keep this comment\nPR0XTEUS_API_TOKEN=keep-me\nCUSTOM_VALUE=keep-me\nPR0XTEUS_RUNTIME_USER="+
			runtimeUser+"\n",
		string(readBootstrapFixture(t, envPath)),
	)
	assert.Contains(t, string(readBootstrapFixture(t, examplePath)), "PR0XTEUS_CONTROLLER_IMAGE=psyb0t/pr0xteus:v9.9.9")
}

func TestBootstrapConfigRefreshesRuntimeTemplatesAndPreservesOperatorConfig(t *testing.T) {
	t.Parallel()

	configDir := t.TempDir()
	_, err := BootstrapConfig(BootstrapOptions{
		ConfigDir:       configDir,
		HostConfigDir:   configDir,
		ControllerImage: defaultControllerImage,
	})
	require.NoError(t, err)

	poolPath := filepath.Join(configDir, poolsRelativePath)
	routingPath := filepath.Join(configDir, routingRelativePath)
	envPath := filepath.Join(configDir, dotEnvFileName)
	composePath := filepath.Join(configDir, composeFileName)
	hostPortsPath := filepath.Join(configDir, hostPortsFileName)
	noHostPortsPath := filepath.Join(configDir, noHostPortsFileName)
	require.NoError(t, os.WriteFile(poolPath, []byte("operator-pool\n"), bootstrapFileMode))
	require.NoError(t, os.WriteFile(routingPath, []byte("operator-routing\n"), bootstrapFileMode))
	require.NoError(t, os.WriteFile(envPath, []byte("PR0XTEUS_API_TOKEN=keep-me\n"), bootstrapFileMode))
	require.NoError(t, os.WriteFile(composePath, []byte("stale-compose\n"), bootstrapFileMode))
	require.NoError(t, os.WriteFile(hostPortsPath, []byte("stale-host-ports\n"), bootstrapFileMode))
	require.NoError(t, os.WriteFile(noHostPortsPath, []byte("stale-no-host-ports\n"), bootstrapFileMode))

	result, err := BootstrapConfig(BootstrapOptions{
		ConfigDir:               configDir,
		HostConfigDir:           configDir,
		ControllerImage:         "psyb0t/pr0xteus:v9.9.9",
		RefreshRuntimeTemplates: true,
	})
	require.NoError(t, err)
	assert.Empty(t, result.Created)
	assert.Equal(
		t,
		[]string{
			composePath,
			hostPortsPath,
			noHostPortsPath,
			filepath.Join(configDir, dotEnvExampleFileName),
		},
		result.Refreshed,
	)
	assert.Equal(t, "operator-pool\n", string(readBootstrapFixture(t, poolPath)))
	assert.Equal(t, "operator-routing\n", string(readBootstrapFixture(t, routingPath)))
	assert.Contains(t, string(readBootstrapFixture(t, envPath)), "PR0XTEUS_API_TOKEN=keep-me\n")
	assert.Contains(t, string(readBootstrapFixture(t, envPath)), "PR0XTEUS_RUNTIME_USER=")
	assert.Equal(t, string(defaultComposeYAML), string(readBootstrapFixture(t, composePath)))
	assert.Equal(t, string(defaultHostPortsComposeYAML), string(readBootstrapFixture(t, hostPortsPath)))
	assert.Equal(t, string(defaultNoHostPortsComposeYAML), string(readBootstrapFixture(t, noHostPortsPath)))
}

func TestWriteBootstrapFile(t *testing.T) {
	t.Parallel()

	t.Run("creates and preserves an operator file", func(t *testing.T) {
		t.Parallel()

		path := filepath.Join(t.TempDir(), "pools.yaml")
		file := bootstrapFile{
			path:         path,
			operatorPath: "/operator/secrets/pools.yaml",
			data:         []byte("pools: {}\n"),
		}
		result := BootstrapResult{}

		require.NoError(t, writeBootstrapFile(file, false, &result))
		assert.Equal(t, []string{file.operatorPath}, result.Created)
		assert.Equal(t, "pools: {}\n", string(readBootstrapFixture(t, path)))

		require.NoError(t, writeBootstrapFile(file, false, &result))
		assert.Equal(t, []string{file.operatorPath}, result.Preserved)
	})

	t.Run("refreshes a runtime template", func(t *testing.T) {
		t.Parallel()

		path := filepath.Join(t.TempDir(), composeFileName)
		require.NoError(t, os.WriteFile(path, []byte("stale\n"), bootstrapFileMode))
		file := bootstrapFile{
			path:         path,
			operatorPath: "/operator/docker-compose.yml",
			data:         []byte("fresh\n"),
			runtime:      true,
		}
		result := BootstrapResult{}

		require.NoError(t, writeBootstrapFile(file, true, &result))
		assert.Equal(t, []string{file.operatorPath}, result.Refreshed)
		assert.Equal(t, "fresh\n", string(readBootstrapFixture(t, path)))
	})

	t.Run("returns a write error", func(t *testing.T) {
		t.Parallel()

		file := bootstrapFile{
			path: filepath.Join(t.TempDir(), "missing", "pools.yaml"),
			data: []byte("pools: {}\n"),
		}

		require.Error(t, writeBootstrapFile(file, false, &BootstrapResult{}))
	})
}

func TestRootEnvExampleMatchesBootstrapTemplate(t *testing.T) {
	t.Parallel()

	rootExamplePath := filepath.Join("..", "..", "..", "..", ".env.example")
	assert.Equal(
		t,
		renderEnvExample(
			"/absolute/path/to/pr0xteus",
			"psyb0t/pr0xteus:vX.Y.Z",
			"1000:1000",
			false,
		),
		string(readBootstrapFixture(t, rootExamplePath)),
	)
}

func TestRootNoHostPortsComposeMatchesBootstrapTemplate(t *testing.T) {
	t.Parallel()

	rootComposePath := filepath.Join("..", "..", "..", "..", noHostPortsFileName)
	assert.Equal(t, string(defaultNoHostPortsComposeYAML), string(readBootstrapFixture(t, rootComposePath)))
}

func TestRootHostPortsComposeMatchesBootstrapTemplate(t *testing.T) {
	t.Parallel()

	rootComposePath := filepath.Join("..", "..", "..", "..", hostPortsFileName)
	assert.Equal(t, string(defaultHostPortsComposeYAML), string(readBootstrapFixture(t, rootComposePath)))
}

func TestRootComposeMatchesBootstrapTemplate(t *testing.T) {
	t.Parallel()

	rootComposePath := filepath.Join("..", "..", "..", "..", composeFileName)
	assert.Equal(t, string(defaultComposeYAML), string(readBootstrapFixture(t, rootComposePath)))
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

func TestCheckedImageReference(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name    string
		value   string
		want    string
		wantErr bool
	}{
		{name: "trims and accepts", value: " psyb0t/pr0xteus:v1 ", want: "psyb0t/pr0xteus:v1"},
		{name: "empty", value: "  ", wantErr: true},
		{name: "embedded space", value: "psyb0t/pr0xteus v1", wantErr: true},
		{name: "embedded newline", value: "psyb0t/pr0xteus\nv1", wantErr: true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := checkedImageReference(tc.value)
			if tc.wantErr {
				require.ErrorIs(t, err, ErrConfigInvalid)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestRenderEnvFileDevelopmentModeUsesLocalControllerImage(t *testing.T) {
	t.Parallel()

	env := renderEnvFile("/abs/config", "ignored/in/dev:mode", "1000:1000", "dev-token", true)
	assert.Contains(t, env, "PR0XTEUS_CONFIG_DIR=/abs/config")
	assert.Contains(t, env, "PR0XTEUS_RUNTIME_USER=1000:1000")
	assert.Contains(t, env, "PR0XTEUS_CONTROLLER_IMAGE="+developmentControllerImage)
	assert.Contains(t, env, apiTokenEnvName+"=dev-token")
	assert.Contains(t, env, "PR0XTEUS_HTTP_HOST_PORT=127.0.0.1:8000")
	assert.Contains(t, env, "PR0XTEUS_DISABLE_HOST_PORTS=false")
	assert.NotContains(t, env, "ignored/in/dev:mode")
	assert.NotContains(t, env, "PR0XTEUS_CELL_IMAGE=")
}

func TestCheckedRuntimeUser(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		value   string
		want    string
		wantErr bool
	}{
		{name: "operator uid and gid", value: "1000:1001", want: "1000:1001"},
		{name: "root", value: "0:0", want: "0:0"},
		{name: "missing separator", value: "1000", wantErr: true},
		{name: "non numeric uid", value: "user:1000", wantErr: true},
		{name: "extra separator", value: "1:2:3", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := checkedRuntimeUser(tc.value)
			if tc.wantErr {
				require.ErrorIs(t, err, ErrConfigInvalid)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestWriteFileIfAbsentRejectsIrregularTargets(t *testing.T) {
	t.Parallel()

	t.Run("existing directory", func(t *testing.T) {
		t.Parallel()

		created, err := writeFileIfAbsent(t.TempDir(), []byte("x"))
		require.ErrorIs(t, err, ErrConfigInvalid)
		assert.False(t, created)
	})

	t.Run("existing symlink", func(t *testing.T) {
		t.Parallel()

		base := t.TempDir()
		target := filepath.Join(base, "target")
		require.NoError(t, os.WriteFile(target, []byte("t"), 0o600))
		link := filepath.Join(base, "link")
		require.NoError(t, os.Symlink(target, link))

		created, err := writeFileIfAbsent(link, []byte("x"))
		require.ErrorIs(t, err, ErrConfigInvalid)
		assert.False(t, created)
	})

	t.Run("creates a new file only once", func(t *testing.T) {
		t.Parallel()

		path := filepath.Join(t.TempDir(), "new")
		created, err := writeFileIfAbsent(path, []byte("hello"))
		require.NoError(t, err)
		assert.True(t, created)

		again, err := writeFileIfAbsent(path, []byte("ignored"))
		require.NoError(t, err)
		assert.False(t, again)
		assert.Equal(t, "hello", string(readBootstrapFixture(t, path)))
	})
}

func TestReplaceDotEnvValue(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		contents string
		want     string
	}{
		{
			name:     "appends after a trailing newline",
			contents: "CUSTOM_VALUE=keep-me\n",
			want:     "CUSTOM_VALUE=keep-me\nPR0XTEUS_RUNTIME_USER=1000:1000\n",
		},
		{
			name:     "appends after an unterminated value",
			contents: "CUSTOM_VALUE=keep-me",
			want:     "CUSTOM_VALUE=keep-me\nPR0XTEUS_RUNTIME_USER=1000:1000\n",
		},
		{
			name:     "replaces every existing value",
			contents: "# keep comments\nPR0XTEUS_RUNTIME_USER=1:1\nPR0XTEUS_RUNTIME_USER=2:2\n",
			want:     "# keep comments\nPR0XTEUS_RUNTIME_USER=1000:1000\nPR0XTEUS_RUNTIME_USER=1000:1000\n",
		},
		{
			name:     "creates a terminated value from empty input",
			contents: "",
			want:     "PR0XTEUS_RUNTIME_USER=1000:1000\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.want, replaceDotEnvValue(tc.contents, "PR0XTEUS_RUNTIME_USER", "1000:1000"))
		})
	}
}

func TestReadRegularDotEnv(t *testing.T) {
	t.Parallel()

	t.Run("reads a regular file", func(t *testing.T) {
		t.Parallel()

		path := filepath.Join(t.TempDir(), dotEnvFileName)
		require.NoError(t, os.WriteFile(path, []byte("A=B\n"), bootstrapFileMode))

		contents, err := readRegularDotEnv(path)
		require.NoError(t, err)
		assert.Equal(t, "A=B\n", string(contents))
	})

	t.Run("rejects a missing file", func(t *testing.T) {
		t.Parallel()

		_, err := readRegularDotEnv(filepath.Join(t.TempDir(), dotEnvFileName))
		require.Error(t, err)
	})

	t.Run("rejects a directory", func(t *testing.T) {
		t.Parallel()

		_, err := readRegularDotEnv(t.TempDir())
		require.ErrorIs(t, err, ErrConfigInvalid)
	})

	t.Run("rejects a symlink", func(t *testing.T) {
		t.Parallel()

		base := t.TempDir()
		target := filepath.Join(base, "target")
		require.NoError(t, os.WriteFile(target, []byte("A=B\n"), bootstrapFileMode))
		link := filepath.Join(base, dotEnvFileName)
		require.NoError(t, os.Symlink(target, link))

		_, err := readRegularDotEnv(link)
		require.ErrorIs(t, err, ErrConfigInvalid)
	})
}

func TestWriteExampleFileRejectsIrregularTargets(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), dotEnvExampleFileName)
	require.NoError(t, os.Mkdir(path, bootstrapDirectoryMode))
	require.ErrorIs(t, writeExampleFile(path, []byte("x")), ErrConfigInvalid)
}

func TestEnsurePrivateDirectoryRejectsNonDirectory(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "file")
	require.NoError(t, os.WriteFile(path, []byte("x"), 0o600))
	require.ErrorIs(t, ensurePrivateDirectory(path), ErrConfigInvalid)
}

func TestCheckedExistingDirectory(t *testing.T) {
	t.Parallel()

	t.Run("rejects a relative path", func(t *testing.T) {
		t.Parallel()

		_, err := checkedExistingDirectory("rel")
		require.ErrorIs(t, err, ErrConfigInvalid)
	})

	t.Run("rejects a file", func(t *testing.T) {
		t.Parallel()

		path := filepath.Join(t.TempDir(), "file")
		require.NoError(t, os.WriteFile(path, []byte("x"), 0o600))
		_, err := checkedExistingDirectory(path)
		require.ErrorIs(t, err, ErrConfigInvalid)
	})

	t.Run("accepts a real directory", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		got, err := checkedExistingDirectory(dir)
		require.NoError(t, err)
		assert.Equal(t, dir, got)
	})
}

func TestBootstrapConfigRejectsInvalidImageAndHostDir(t *testing.T) {
	t.Parallel()

	t.Run("blank controller image", func(t *testing.T) {
		t.Parallel()

		_, err := BootstrapConfig(BootstrapOptions{
			ConfigDir:       t.TempDir(),
			HostConfigDir:   t.TempDir(),
			ControllerImage: "  ",
		})
		require.ErrorIs(t, err, ErrConfigInvalid)
	})

	t.Run("relative host config dir", func(t *testing.T) {
		t.Parallel()

		_, err := BootstrapConfig(BootstrapOptions{
			ConfigDir:       t.TempDir(),
			HostConfigDir:   "relative/host",
			ControllerImage: defaultControllerImage,
		})
		require.ErrorIs(t, err, ErrConfigInvalid)
	})
}

func TestCheckConfigRejectsInvalidOperatorFiles(t *testing.T) {
	t.Parallel()

	seed := func(t *testing.T) string {
		t.Helper()

		configDir := t.TempDir()
		_, err := BootstrapConfig(BootstrapOptions{
			ConfigDir:       configDir,
			HostConfigDir:   configDir,
			ControllerImage: defaultControllerImage,
		})
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(
			filepath.Join(configDir, bundleRelativePath, "de.conf"),
			[]byte("[Interface]\n"), 0o600,
		))

		return configDir
	}

	t.Run("no pools defined", func(t *testing.T) {
		t.Parallel()

		configDir := seed(t)
		require.NoError(t, os.WriteFile(
			filepath.Join(configDir, poolsRelativePath),
			[]byte("pools: {}\n"), 0o600,
		))
		require.ErrorIs(t,
			CheckConfig(ConfigCheckOptions{ConfigDir: configDir}),
			ErrConfigInvalid,
		)
	})

	t.Run("routing names an unknown pool", func(t *testing.T) {
		t.Parallel()

		configDir := seed(t)
		require.NoError(t, os.WriteFile(
			filepath.Join(configDir, poolsRelativePath),
			[]byte("pools:\n  eu:\n    configs: [de]\n    exit_countries:\n      de: DE\n"),
			0o600,
		))
		require.NoError(t, os.WriteFile(
			filepath.Join(configDir, routingRelativePath),
			[]byte("country_to_pool:\n  DE: missing\n"), 0o600,
		))
		require.ErrorIs(t,
			CheckConfig(ConfigCheckOptions{ConfigDir: configDir}),
			ErrConfigInvalid,
		)
	})
}

func TestLoadAPITokenFromDotEnv(t *testing.T) {
	t.Parallel()

	t.Run("skips comments and blanks and strips quotes", func(t *testing.T) {
		t.Parallel()

		path := filepath.Join(t.TempDir(), dotEnvFileName)
		require.NoError(t, os.WriteFile(path, []byte(
			"# generated file\n\nPR0XTEUS_CONFIG_DIR=/x\n"+
				apiTokenEnvName+"=\"tok-value\"\n",
		), 0o600))

		token, err := loadAPITokenFromDotEnv(path)
		require.NoError(t, err)
		assert.Equal(t, "tok-value", string(token))
	})

	t.Run("missing token key is rejected", func(t *testing.T) {
		t.Parallel()

		path := filepath.Join(t.TempDir(), dotEnvFileName)
		require.NoError(t, os.WriteFile(path, []byte("# only comments\nOTHER=1\n"), 0o600))

		_, err := loadAPITokenFromDotEnv(path)
		require.ErrorIs(t, err, ErrConfigInvalid)
	})

	t.Run("missing file errors", func(t *testing.T) {
		t.Parallel()

		_, err := loadAPITokenFromDotEnv(filepath.Join(t.TempDir(), "absent"))
		require.Error(t, err)
	})
}
