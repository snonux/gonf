package api

import (
	"fmt"

	"github.com/snonux/gonf/internal/inventory"
	"github.com/snonux/gonf/internal/logger"
)

// MustHostValue returns the value stored under key on the named host, typed as
// T. Missing key or wrong type fails fast via logger.Fatal (exit 1) — the same
// contract as MustHost / MustCluster. Prefer WithValue / SetValue at inventory
// time over a parallel map + MustMapValue in the recipe. Every failure message
// starts with "MustHostValue: " (see lookupHostValue for the rest).
func MustHostValue[T any](host, key string) T {
	v, err := lookupHostValue[T](host, key)
	if err != nil {
		logger.Fatal("MustHostValue: %v", err)
	}
	return v
}

// lookupHostValue is the non-fatal core of MustHostValue, shared with
// ForHosts, which must report the same inventory errors as a recording error
// instead of exiting. The error names the host, the key and, for a type
// mismatch, both types; it never contains the value itself.
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
