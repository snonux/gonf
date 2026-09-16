package api

import "github.com/snonux/gonf/internal/logger"

// MustMapValue returns m[key], or fails fast via logger.Fatal when the key
// is missing. Use it when iterating FleetRef.HostNames() against a per-host
// schedule/config map so a host added to the fleet without a map entry aborts
// the recipe before apply (same contract as MustHost / MustFleet).
func MustMapValue[K comparable, V any](m map[K]V, key K, what string) V {
	v, ok := m[key]
	if !ok {
		logger.Fatal("%s: no entry for %v", what, key)
	}
	return v
}
