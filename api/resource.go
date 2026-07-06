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
}

func Apply() error {
	return internal.Apply()
}
