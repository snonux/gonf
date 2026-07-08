package api

import (
	internal "codeberg.org/snonux/gonf/internal/resource"
)

// Resource represents a system resource managed by gonfs.
// It is an interface to hide the internal implementation details
// of the resource registry.
type Resource interface {
	ID() string
	String() string
	// Dependencies returns the flattened resource IDs this value represents,
	// so it can be passed to the DependsOn option. A single resource yields
	// its own ID; a Multi yields the IDs of all its members.
	Dependencies() []string
}

func Apply() error {
	return internal.Apply()
}
