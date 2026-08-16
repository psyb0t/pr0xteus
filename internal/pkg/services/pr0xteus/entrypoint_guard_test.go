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

// TestCellEntrypoint_BindsSOCKS5ToEgressAndControlToControlInterface guards the
// dual-network firewall: SOCKS5 is accepted on the egress interface (where
// callers consume the socks5:// URL) while the control port is accepted on the
// derived control interface (the internal cell-control network in dual-network
// mode), and never on wg0. The exact rule blocks lock the pairing so a control
// rule can't silently drift back onto the egress interface.
func TestCellEntrypoint_BindsSOCKS5ToEgressAndControlToControlInterface(t *testing.T) {
	t.Parallel()

	contents := readEntrypointScript(t)

	// The control interface is derived, never hardcoded to eth0.
	assert.Contains(t, contents, "CONTROL_IF=$(ip -o -4 addr show")

	// SOCKS5 accepted on the egress interface.
	assert.Contains(
		t,
		contents,
		"-A INPUT -i \"${EGRESS_IF}\" \\\n\t-p tcp --dport \"${SOCKS5_PORT}\" -j ACCEPT",
	)

	// Control port accepted on the control interface, not egress.
	assert.Contains(
		t,
		contents,
		"-A INPUT -i \"${CONTROL_IF}\" \\\n\t-p tcp --dport \"${CONTROL_PORT}\" -j ACCEPT",
	)
}

func readEntrypointScript(t *testing.T) string {
	t.Helper()

	raw, err := os.ReadFile(entrypointScriptPath)
	require.NoError(t, err)

	return string(raw)
}
