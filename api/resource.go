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

// Apply applies every resource registered so far, in dependency order.
// Task methods call the DSL constructors to register resources, and one
// Apply call reconciles the system against all of them.
func Apply() error {
	return resource.Apply()
}
