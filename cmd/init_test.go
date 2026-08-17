package main

import (
	"testing"

	"github.com/psyb0t/gonfiguration"
	pr0xteus "github.com/psyb0t/pr0xteus/internal/pkg/services/pr0xteus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConfigurePr0xteusCellImage(t *testing.T) {
	gonfiguration.Reset()
	t.Cleanup(gonfiguration.Reset)
	configurePr0xteusCellImage(testVersion)
	t.Cleanup(func() {
		configurePr0xteusCellImage("dev")
	})
	t.Setenv("PR0XTEUS_API_TOKEN", "test-only-token")
	t.Setenv("PR0XTEUS_MANAGED_SCOPE", "unit-test")

	config, err := pr0xteus.LoadConfig()
	require.NoError(t, err)
	assert.Equal(t, "psyb0t/pr0xteus:cell-"+testVersion, config.CellImage)
}
