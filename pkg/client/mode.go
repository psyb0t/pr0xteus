package client

// Mode controls how Client.Do dispatches a request.
type Mode int

const (
	// ModeVPNOnly forces every request through pr0xteus's SOCKS5
	// proxy. The transport it builds simply has no direct-dial
	// code path — there is nothing for a request to escape
	// through. Default for any consumer where the host's public IP
	// must NEVER touch the target.
	ModeVPNOnly Mode = iota

	// ModePublicFirst tries the host's direct internet first;
	// on rate-limit (429), forbidden (403), service-unavailable
	// (503), or transport error, the same request is retried
	// through the SOCKS5 proxy.
	//
	// Use when the host's public IP buys preferential treatment
	// for the target (e.g. registered API feeder, allowlisted
	// internal endpoint).
	ModePublicFirst
)

// String returns a short human-readable mode name.
func (m Mode) String() string {
	switch m {
	case ModeVPNOnly:
		return "vpn_only"
	case ModePublicFirst:
		return "public_first"
	}

	return "unknown"
}

// WithMode sets the dispatch mode. Defaults to ModeVPNOnly when
// unset — callers must explicitly opt in to public-first.
func WithMode(m Mode) Option {
	return func(c *Client) { c.mode = m }
}
