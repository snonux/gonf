package configset

import (
	"fmt"

	"github.com/snonux/gonf/plan"
)

// expanded returns a copy of s whose member paths, chroot and staging
// directory have their ${HOME} path tokens (api.DestHome) resolved on the
// running host (plan.ExpandPath). A set is recorded and registered with its
// tokens intact, so the destination resolves them against its own home; the
// expanded copy is what is validated and applied. Validation needs it
// because the checks compare clean absolute paths, which a token path is
// not until expanded. On the controller (build, ToOp) the expansion uses the
// controller's home only to validate the shape; the destination
// (setHandler.Apply) re-expands and re-validates with its own.
//
// Member contents and validator arguments are not expanded: they are
// payload, and member paths reach them through gonf's own member tokens
// (MemberPath), which already render the expanded paths.
func (s *spec) expanded() (*spec, error) {
	out := *s
	var err error
	if out.chroot, err = plan.ExpandPath(s.chroot); err != nil {
		return nil, fmt.Errorf("config set %s: chroot: %w", s.name, err)
	}
	if out.stagingDir, err = plan.ExpandPath(s.stagingDir); err != nil {
		return nil, fmt.Errorf("config set %s: staging directory: %w", s.name, err)
	}
	out.members = make([]memberSpec, len(s.members))
	for i, m := range s.members {
		if m.path, err = plan.ExpandPath(m.path); err != nil {
			return nil, fmt.Errorf("config set %s: member %s: %w", s.name, m.key, err)
		}
		out.members[i] = m
	}
	return &out, nil
}

// validateExpanded validates s with its path tokens expanded on the running
// host and returns the expanded copy, ready to apply here.
func (s *spec) validateExpanded() (*spec, error) {
	exp, err := s.expanded()
	if err != nil {
		return nil, err
	}
	if err := exp.validate(); err != nil {
		return nil, err
	}
	return exp, nil
}
