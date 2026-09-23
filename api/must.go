package api

import (
	"fmt"

	"github.com/snonux/gonf/internal/declerr"
	"github.com/snonux/gonf/internal/inventory"
)

// MustHostValue returns the value stored under key on the named host, typed as
// T. A missing host or key or a wrong type is reported as a declaration error
// (internal/declerr: it fails the record, or refuses Apply and the CLI) and the
// zero T is returned — the same contract as MustHost / MustCluster. Prefer
// WithValue / SetValue at inventory time over a parallel map + MustMapValue in
// the recipe. Every failure message starts with "MustHostValue: " (see
// lookupHostValue for the rest).
func MustHostValue[T any](host, key string) T {
	v, err := lookupHostValue[T](host, key)
	if err != nil {
		declerr.Reportf("MustHostValue: %w", err)
	}
	return v
}

// lookupHostValue is the error-returning core of MustHostValue, shared with
// ForHosts, which reports the same inventory errors with its own prefix. The
// error names the host, the key and, for a type mismatch, both types; it never
// contains the value itself.
func lookupHostValue[T any](host, key string) (T, error) {
	var zero T
	if key == "" {
		return zero, fmt.Errorf("key must not be empty")
	}
	raw, hostFound, keyFound := inventory.HostValue(host, key)
	if !hostFound {
		return zero, fmt.Errorf("Host %q is not registered", host)
	}
	if !keyFound {
		return zero, fmt.Errorf("Host %q: no value %q", host, key)
	}
	v, ok := raw.(T)
	if !ok {
		return zero, fmt.Errorf("Host %q value %q: want %T, got %T", host, key, zero, raw)
	}
	return v, nil
}
