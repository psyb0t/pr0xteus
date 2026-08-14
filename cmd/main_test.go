package main

import (
	"bytes"
	"testing"

	"github.com/psyb0t/ctxscope"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testBinary = "test-binary"
	testCommit = "deadbeef"
)

func TestSetProcessScope(t *testing.T) {
	ctxscope.RemoveGlobal(scopeKeyBinary, scopeKeyCommit)
	t.Cleanup(func() {
		ctxscope.RemoveGlobal(scopeKeyBinary, scopeKeyCommit)
	})

	setProcessScope(testBinary, testCommit)

	scope := ctxscope.GetGlobal()
	assert.Equal(t, testBinary, scope[scopeKeyBinary])
	assert.Equal(t, testCommit, scope[scopeKeyCommit])
}

func TestBuildRootCommandUsesPr0xteusByDefault(t *testing.T) {
	t.Parallel()

	command := buildRootCommand()

	assert.Equal(t, "pr0xteus", command.Use)
	assert.Equal(t, "pr0xteus", command.Short)
}

func TestBuildRootCommandIncludesConfigCommands(t *testing.T) {
	t.Parallel()

	command := buildRootCommand()
	configCheck, _, err := command.Find([]string{"config", "check"})

	assert.NoError(t, err)
	assert.Equal(t, "check", configCheck.Name())
}

func TestConfigInitCommandCreatesConfiguration(t *testing.T) {
	t.Parallel()

	configDir := t.TempDir()
	command := buildRootCommand()
	output := &bytes.Buffer{}
	command.SetOut(output)
	command.SetErr(output)
	command.SetArgs([]string{
		"config",
		"init",
		"--config-dir",
		configDir,
		"--host-config-dir",
		configDir,
	})

	require.NoError(t, command.Execute())
	assert.Contains(t, output.String(), "created "+configDir+"/.env")
}

func TestConfigCheckCommandRejectsMissingConfiguration(t *testing.T) {
	t.Parallel()

	command := buildRootCommand()
	command.SetArgs([]string{
		"config",
		"check",
		"--config-dir",
		t.TempDir(),
	})

	require.Error(t, command.Execute())
}
