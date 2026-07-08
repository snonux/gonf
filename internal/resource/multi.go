package resource

import (
	"errors"
	"strings"
)

// Multi is a collection of resources that satisfies the api.Resource interface.
type Multi []Resource

func (m Multi) String() string {
	strs := make([]string, 0, len(m))

	for _, res := range m {
		strs = append(strs, res.String())
	}

	return strings.Join(strs, ", ")
}

func (m Multi) ID() string {
	ids := make([]string, 0, len(m))

	for _, res := range m {
		ids = append(ids, res.String())
	}

	return strings.Join(ids, "+")
}

func (m Multi) Apply() error {
	var errs []error

	for _, res := range m {
		errs = append(errs, res.Apply())
	}

	return errors.Join(errs...)
}
