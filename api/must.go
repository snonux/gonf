package api

import "github.com/snonux/gonf/internal/logger"

// MustHostValue returns the value stored under key on the named host, typed as
// T. Missing key or wrong type fails fast via logger.Fatal (exit 1) — the same
// contract as MustHost / MustCluster. Prefer WithValue / SetValue at inventory
// time over a parallel map + MustMapValue in the recipe.
func MustHostValue[T any](host, key string) T {
	inventoryMu.Lock()
	defer inventoryMu.Unlock()
	rec, ok := hostsByName[host]
	if !ok {
		logger.Fatal("MustHostValue: Host %q is not registered", host)
	}
	if key == "" {
		logger.Fatal("MustHostValue: key must not be empty")
	}
	raw, ok := rec.values[key]
	if !ok {
		logger.Fatal("Host %q: no value %q", host, key)
	}
	v, ok := raw.(T)
	if !ok {
		var zero T
		logger.Fatal("Host %q value %q: want %T, got %T", host, key, zero, raw)
	}
	return v
}
