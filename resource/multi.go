package resource

import (
	"errors"
	"strings"
)

// Dependency is implemented by anything that can be depended upon. It returns
// the flattened list of individual resource IDs, so that depending on a Multi
// expands into a dependency on each of its members individually.
type Dependency interface {
	Dependencies() []string
}

// Multi is a collection of resources that satisfies the api.Resource interface.
type Multi []Resource

// String returns the comma-joined member IDs.
func (m Multi) String() string {
	strs := make([]string, 0, len(m))

	for _, res := range m {
		strs = append(strs, res.String())
	}

	return strings.Join(strs, ", ")
}

// ID returns the member IDs joined by "+"; a Multi has no single identity.
func (m Multi) ID() string {
	ids := make([]string, 0, len(m))

	for _, res := range m {
		ids = append(ids, res.String())
	}

	return strings.Join(ids, "+")
}

// Dependencies flattens the Multi into the IDs of each of its members, so a
// dependency on a Multi becomes an individual dependency on every resource it
// contains.
func (m Multi) Dependencies() []string {
	ids := make([]string, 0, len(m))

	for _, res := range m {
		ids = append(ids, res.Dependencies()...)
	}

	return ids
}

// Apply applies every member in order, joining any errors so one failure
// does not mask the rest.
func (m Multi) Apply() error {
	var errs []error

	for _, res := range m {
		errs = append(errs, res.Apply())
	}

	return errors.Join(errs...)
}
