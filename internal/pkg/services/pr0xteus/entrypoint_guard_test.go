package pr0xteus

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// entrypointScriptPath resolves cell/entrypoint.sh relative to this package
// (internal/pkg/services/pr0xteus), four directories below the repo root.
const entrypointScriptPath = "../../../../cell/entrypoint.sh"

// TestCellEntrypoint_DoesNotAcceptInboundFromTheTunnelSide guards against
// regressing the removed `-A INPUT -i wg0 -j ACCEPT` rule: accepting new
// inbound on wg0 would expose cellproxy's SOCKS5 + control ports to the
// egress (tunnel) side instead of only the docker network the controller
// shares.
func TestCellEntrypoint_DoesNotAcceptInboundFromTheTunnelSide(t *testing.T) {
	t.Parallel()

	contents := readEntrypointScript(t)

	// The doc comment above the OUTPUT-only wg0 rule intentionally mentions
	// the removed form in prose, so match the full command (with the
	// "iptables" invocation) rather than the bare flag fragment.
	assert.NotContains(t, contents, `iptables -A INPUT -i wg0 -j ACCEPT`)
}

// TestCellEntrypoint_AcceptsSOCKS5AndControlOnlyOnEgressInterface guards the
// replacement rules: inbound SOCKS5 + control traffic is accepted only on
// the docker network interface (EGRESS_IF), never on wg0.
func TestCellEntrypoint_AcceptsSOCKS5AndControlOnlyOnEgressInterface(t *testing.T) {
	t.Parallel()

	contents := readEntrypointScript(t)

	assert.Contains(t, contents, `-A INPUT -i "${EGRESS_IF}"`)
	assert.Contains(t, contents, `--dport "${SOCKS5_PORT}"`)
	assert.Contains(t, contents, `--dport "${CONTROL_PORT}"`)
}

func readEntrypointScript(t *testing.T) string {
	t.Helper()

	raw, err := os.ReadFile(entrypointScriptPath)
	require.NoError(t, err)

	return string(raw)
}
