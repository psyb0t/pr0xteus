package pr0xteus

import (
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLeaseRegistry_IssueLookupAndExpiry(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.August, 16, 22, 0, 0, 0, time.UTC)
	registry := newLeaseRegistry("127.0.0.1:1080", time.Minute)
	registry.now = func() time.Time { return now }
	internalURL, err := url.Parse("socks5://cell-private:1080")
	require.NoError(t, err)

	lease, err := registry.Issue(Acquisition{
		Pool: "western",
		Tunnel: &Tunnel{
			ContainerID: "cell-1",
			ProxyURL:    internalURL,
			GatewayAddr: "172.28.0.4:1080",
		},
	})
	require.NoError(t, err)
	assert.Equal(t, now.Add(time.Minute), lease.ExpiresAt)

	issuedURL, err := url.Parse(lease.URL)
	require.NoError(t, err)
	assert.Equal(t, proxySchemeSOCKS5, issuedURL.Scheme)
	assert.Equal(t, "127.0.0.1:1080", issuedURL.Host)
	password, ok := issuedURL.User.Password()
	require.True(t, ok)

	record, ok := registry.Lookup(issuedURL.User.Username(), password)
	require.True(t, ok)
	assert.Equal(t, "western", record.Pool)
	assert.Equal(t, "cell-1", record.ContainerID)
	assert.Equal(t, "socks5://cell-private:1080", record.InternalURL.String())
	assert.NotEqual(t, password, string(record.SecretDigest[:]))

	record.InternalURL.Host = "mutated:1080"
	record, ok = registry.Lookup(issuedURL.User.Username(), password)
	require.True(t, ok)
	assert.Equal(t, "cell-private:1080", record.InternalURL.Host)

	now = now.Add(time.Minute)
	_, ok = registry.Lookup(issuedURL.User.Username(), password)
	assert.False(t, ok)
	assert.Empty(t, registry.leases)
}

func TestLeaseRegistry_RejectsIncompleteAcquisition(t *testing.T) {
	t.Parallel()

	registry := newLeaseRegistry("127.0.0.1:1080", time.Minute)
	internalURL, err := url.Parse("socks5://cell-private:1080")
	require.NoError(t, err)

	testCases := []struct {
		name string
		acq  Acquisition
	}{
		{name: "no tunnel"},
		{name: "no pool", acq: Acquisition{Tunnel: &Tunnel{ContainerID: "cell", ProxyURL: internalURL, GatewayAddr: "cell:1080"}}},
		{name: "no container", acq: Acquisition{Pool: "western", Tunnel: &Tunnel{ProxyURL: internalURL, GatewayAddr: "cell:1080"}}},
		{name: "no internal url", acq: Acquisition{Pool: "western", Tunnel: &Tunnel{ContainerID: "cell", GatewayAddr: "cell:1080"}}},
		{name: "no gateway", acq: Acquisition{Pool: "western", Tunnel: &Tunnel{ContainerID: "cell", ProxyURL: internalURL}}},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := registry.Issue(tc.acq)
			require.ErrorIs(t, err, ErrPoolUnavailable)
		})
	}
}
