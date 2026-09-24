package service

// WithFlags support: the optional backend capability that reads and writes
// a service's startup flags, and the policy that turns a flags difference
// into one action run between enable and start/restart (see
// Service.applyWith in converge.go).

import (
	"errors"
	"fmt"
	"regexp"

	"github.com/snonux/gonf/resource"
)

// flagger is the backend capability behind WithFlags. The BSD backends
// implement it (rcctl, FreeBSD sysrc, NetBSD rc.conf); systemd does not,
// since a unit has no flags variable, so a Service with WithFlags is
// refused there before anything runs (checkFlagsSupport). It is a separate
// interface, not part of backend, so a backend without flags carries no
// stub methods.
type flagger interface {
	// flagsMatch reports whether the service's configured flags already
	// equal want. A backend that cannot tell (an unparseable rc.conf line)
	// reports false, so the flags are rewritten to a known form.
	flagsMatch(u unit, want string) (bool, error)
	// setFlags stores flags as the service's startup flags.
	setFlags(u unit, flags string) error
	// describeFlags returns the log text for setting flags, like
	// backend.describe does for a verb.
	describeFlags(u unit, flags string) (would, did string)
}

// errFlagsUnsupported is the WithFlags refusal of a backend without a
// flagger (systemd). The wording is user-visible and pinned by tests.
var errFlagsUnsupported = errors.New("WithFlags is only supported on the BSD rc backends (rcctl, FreeBSD, NetBSD); " +
	"systemd units have no flags setting, use a drop-in instead")

// checkFlagsSupport refuses a Service with WithFlags on a backend that has
// no flagger, before any probe or action runs.
func (s *Service) checkFlagsSupport(b backend) error {
	if !s.hasFlags {
		return nil
	}
	if _, ok := b.(flagger); !ok {
		return fmt.Errorf("service[%s]: %w", s.name, errFlagsUnsupported)
	}
	return nil
}

// checkFlagsDeclaration refuses WithFlags on an absent service, since a
// stopped and disabled service has no startup flags to converge. Present
// and EnsureWith call it right after the option misuse check, so the
// refusal is a declaration error like any option misuse.
func (s *Service) checkFlagsDeclaration() error {
	if s.hasFlags && s.Absent {
		return fmt.Errorf("service %s: WithFlags cannot be used with an absent service", s.name)
	}
	return nil
}

// flagsUpdate returns the action that sets s's flags through b, or nil when
// s has no WithFlags or its flags already match. A service that is not yet
// enabled always gets the action: enabling can itself rewrite the flags
// (rcctl enable keeps or resets them), so a probe taken before the enable
// would not show what the service starts with.
func (s *Service) flagsUpdate(b backend, u unit, enabled bool) (resource.Action, error) {
	if !s.hasFlags {
		return nil, nil
	}
	f, ok := b.(flagger)
	if !ok {
		return nil, fmt.Errorf("service[%s]: %w", s.name, errFlagsUnsupported)
	}
	if enabled {
		match, err := f.flagsMatch(u, s.flags)
		if err != nil {
			return nil, err
		}
		if match {
			return nil, nil
		}
	}
	return flagsAction{f: f, u: u, flags: s.flags}, nil
}

// sequence turns verbs into the actions Converge runs, placing flags (when
// not nil) after an enable and before everything else, so a daemon always
// starts, restarts or reloads with its new flags.
func sequence(b backend, u unit, verbs []verb, flags resource.Action) []resource.Action {
	actions := make([]resource.Action, 0, len(verbs)+1)
	for _, v := range verbs {
		if flags != nil && v != verbEnable {
			actions = append(actions, flags)
			flags = nil
		}
		actions = append(actions, backendAction{b: b, u: u, v: v})
	}
	if flags != nil {
		actions = append(actions, flags)
	}
	return actions
}

// flagsAction adapts setting a service's flags to resource.Action.
type flagsAction struct {
	f     flagger
	u     unit
	flags string
}

// Do stores the flags through the backend.
func (a flagsAction) Do() error { return a.f.setFlags(a.u, a.flags) }

// Describe returns the backend's log text for setting the flags.
func (a flagsAction) Describe() (would, did string) { return a.f.describeFlags(a.u, a.flags) }

// rcVarName matches a service name usable in an rc.conf variable name
// (NAME_flags): a shell identifier.
var rcVarName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// flagsVar returns name's rc.conf flags variable, NAME_flags, refusing a
// name that cannot form a shell variable (FreeBSD sysrc and NetBSD rc.conf
// both need one).
func flagsVar(name string) (string, error) {
	if !rcVarName.MatchString(name) {
		return "", fmt.Errorf("service[%s]: WithFlags needs a service name that is a shell identifier, for its %s_flags variable", name, name)
	}
	return name + "_flags", nil
}
