package api

import (
	"github.com/snonux/gonf/resource"
)

// Resource represents a system resource managed by gonf.
type Resource interface {
	ID() string
	String() string
	// Dependencies returns the flattened resource IDs this value represents,
	// so it can be passed to the DependsOn option. A single resource yields
	// its own ID; a Multi yields the IDs of all its members.
	Dependencies() []string
}

func Apply() error {
	return resource.Apply()
}
