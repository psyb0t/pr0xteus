package pr0xteus

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"net/url"
	"sync"
	"time"

	"github.com/psyb0t/ctxerrors"
)

const proxyLeaseSecretBytes = 32

// ProxyLease is the short-lived SOCKS5 capability issued after an allocation.
// The raw secret exists only in URL returned to the authenticated caller; the
// registry retains a digest so logs and in-memory diagnostics cannot recover it.
type ProxyLease struct {
	URL       string
	ExpiresAt time.Time
}

type proxyLeaseRecord struct {
	Pool         string
	ContainerID  string
	InternalURL  *url.URL
	GatewayAddr  string
	SecretDigest [sha256.Size]byte
	ExpiresAt    time.Time
}

type leaseRegistry struct {
	mu         sync.Mutex
	publicAddr string
	ttl        time.Duration
	now        func() time.Time
	leases     map[string]proxyLeaseRecord
}

func newLeaseRegistry(publicAddr string, ttl time.Duration) *leaseRegistry {
	return &leaseRegistry{
		publicAddr: publicAddr,
		ttl:        ttl,
		now:        time.Now,
		leases:     make(map[string]proxyLeaseRecord),
	}
}

func (r *leaseRegistry) Issue(acq Acquisition) (ProxyLease, error) {
	if acq.Tunnel == nil || acq.Pool == "" || acq.Tunnel.ContainerID == "" ||
		acq.Tunnel.ProxyURL == nil || acq.Tunnel.GatewayAddr == "" {
		return ProxyLease{}, ctxerrors.Wrap(
			ErrPoolUnavailable, "allocated tunnel has no controller gateway",
		)
	}

	leaseID, err := newLeasePart()
	if err != nil {
		return ProxyLease{}, ctxerrors.Wrap(err, "generate proxy lease ID")
	}

	secret, err := newLeasePart()
	if err != nil {
		return ProxyLease{}, ctxerrors.Wrap(err, "generate proxy lease secret")
	}

	now := r.now()
	expiresAt := now.Add(r.ttl)
	secretDigest := sha256.Sum256([]byte(secret))

	r.mu.Lock()
	r.pruneExpiredLocked(now)
	r.leases[leaseID] = proxyLeaseRecord{
		Pool:         acq.Pool,
		ContainerID:  acq.Tunnel.ContainerID,
		InternalURL:  cloneProxyURL(acq.Tunnel.ProxyURL),
		GatewayAddr:  acq.Tunnel.GatewayAddr,
		SecretDigest: secretDigest,
		ExpiresAt:    expiresAt,
	}
	r.mu.Unlock()

	leaseURL := (&url.URL{
		Scheme: proxySchemeSOCKS5,
		Host:   r.publicAddr,
		User:   url.UserPassword(leaseID, secret),
	}).String()

	return ProxyLease{URL: leaseURL, ExpiresAt: expiresAt}, nil
}

func (r *leaseRegistry) Lookup(leaseID, secret string) (proxyLeaseRecord, bool) {
	providedDigest := sha256.Sum256([]byte(secret))
	now := r.now()

	r.mu.Lock()
	defer r.mu.Unlock()

	r.pruneExpiredLocked(now)

	record, ok := r.leases[leaseID]
	if !ok || subtle.ConstantTimeCompare(record.SecretDigest[:], providedDigest[:]) != 1 {
		return proxyLeaseRecord{}, false
	}

	record.InternalURL = cloneProxyURL(record.InternalURL)

	return record, true
}

func (r *leaseRegistry) pruneExpiredLocked(now time.Time) {
	for leaseID, record := range r.leases {
		if !now.Before(record.ExpiresAt) {
			delete(r.leases, leaseID)
		}
	}
}

func newLeasePart() (string, error) {
	bytes := make([]byte, proxyLeaseSecretBytes)
	if _, err := rand.Read(bytes); err != nil {
		return "", ctxerrors.Wrap(err, "generate lease secret")
	}

	return base64.RawURLEncoding.EncodeToString(bytes), nil
}

func cloneProxyURL(value *url.URL) *url.URL {
	if value == nil {
		return nil
	}

	copied := *value

	return &copied
}
