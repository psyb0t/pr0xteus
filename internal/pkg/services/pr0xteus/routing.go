package pr0xteus

import (
	"os"
	"sort"
	"strings"

	"github.com/psyb0t/ctxerrors"
	"gopkg.in/yaml.v3"
)

// routingFile is the on-disk YAML shape for egress-routing.yaml.
// snake_case is the operator-facing convention; tagliatelle would
// flag this but the YAML file shape is owned by the operator.
//
//nolint:tagliatelle // operator-managed YAML uses snake_case
type routingFile struct {
	CountryToPool map[string]string `yaml:"country_to_pool"`
	DefaultPool   string            `yaml:"default_pool"`
}

// Router resolves an ISO 3166-1 alpha-2 country code to a logical
// pool name. Loaded once from egress-routing.yaml at startup; no
// hot-reload in v1 (operator can SIGTERM + restart the service to
// pick up changes).
type Router struct {
	// countryToPool is the parsed map. Keys are lowercased.
	countryToPool map[string]string

	// defaultPool is the fallback when the country has no entry.
	// Empty string means "no default" → a country request returns
	// ErrInvalidCountry for unmapped countries.
	defaultPool string
}

// LoadRouter reads + validates egress-routing.yaml. fallbackDefault
// is the Config.DefaultPool; the file's default_pool takes
// precedence if present + non-empty.
func LoadRouter(
	path, fallbackDefault string, knownPools map[string]struct{},
) (*Router, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, ctxerrors.Wrapf(
			ErrConfigInvalid, "read %s: %s", path, err.Error(),
		)
	}

	var parsed routingFile
	if err := yaml.Unmarshal(raw, &parsed); err != nil {
		return nil, ctxerrors.Wrapf(
			ErrConfigInvalid, "yaml unmarshal %s: %s", path, err.Error(),
		)
	}

	// Normalize keys to lowercase so lookups don't double-allocate
	// per request. Operators write "ru" or "RU" — both work.
	normalized := make(map[string]string, len(parsed.CountryToPool))
	for cc, pool := range parsed.CountryToPool {
		key := strings.ToLower(strings.TrimSpace(cc))

		if _, ok := knownPools[pool]; !ok {
			return nil, ctxerrors.Wrapf(
				ErrConfigInvalid,
				"country %s routes to unknown pool %q", key, pool,
			)
		}

		normalized[key] = pool
	}

	defaultPool := parsed.DefaultPool
	if defaultPool == "" {
		defaultPool = fallbackDefault
	}

	if defaultPool != "" {
		if _, ok := knownPools[defaultPool]; !ok {
			return nil, ctxerrors.Wrapf(
				ErrConfigInvalid,
				"default pool %q is not in pools.yaml", defaultPool,
			)
		}
	}

	return &Router{
		countryToPool: normalized,
		defaultPool:   defaultPool,
	}, nil
}

// Resolve returns the pool name for the given country code, falling
// back to the configured default when the country is unmapped.
// Returns ErrInvalidCountry when no mapping exists AND no default
// is configured.
func (r *Router) Resolve(country string) (string, error) {
	key := strings.ToLower(strings.TrimSpace(country))

	if pool, ok := r.countryToPool[key]; ok {
		return pool, nil
	}

	if r.defaultPool == "" {
		return "", ctxerrors.Wrapf(
			ErrInvalidCountry,
			"country %q has no pool and no default configured", key,
		)
	}

	return r.defaultPool, nil
}

// DefaultPool returns the configured fallback pool name. Empty
// string when no default is set.
func (r *Router) DefaultPool() string { return r.defaultPool }

// Countries returns the sorted list of country codes that have an
// explicit pool mapping. Useful for the /v1/pools operator view and tests.
func (r *Router) Countries() []string {
	out := make([]string, 0, len(r.countryToPool))
	for cc := range r.countryToPool {
		out = append(out, cc)
	}

	sort.Strings(out)

	return out
}
