package api

import (
	"fmt"
	"slices"

	"github.com/snonux/gonf/internal/declerr"
)

// Alias registers name as a second public name for the task target, e.g. a
// legacy name kept for compatibility:
//
//	Alias("home_prompts", "Legacy alias for home_agents", "home_agents")
//
// An alias is callable everywhere a task name is accepted (Run, the CLI,
// `gonf plan`, push/cluster/fleet) and records exactly the target's body,
// with the target's conditions, privilege (Privileged chunks) and cluster —
// an alias has no options of its own, so it can never widen or narrow what
// the target does. It is listed (-list, Tasks, Matching) under its own name
// and description whenever the target is active.
//
// Unlike a task whose body only calls Run(target), an alias takes part in
// aggregates as its target: an aggregate that reaches both the alias and the
// target records the target once, at the position where either first
// appears (see recordAggregate for the exact rule). A pattern aggregate
// never includes an alias of itself.
//
// The target may be registered before or after the alias; it is resolved
// when a plan is recorded. An unknown target or a target that is itself an
// alias fails that record with an error naming both (and such an alias is
// never listed). Aliases share the task namespace: registering a name twice,
// an empty name or target, an alias of itself, or an alias of an
// AggregateTasks that lists the alias as a member (the aggregate would
// include itself) is reported as a declaration error (internal/declerr) and
// not queued, like any other registration-time misuse.
func Alias(name, description, target string) {
	if err := checkAlias(name, target); err != nil {
		declerr.Report(err)
		return
	}
	queueCandidate(taskCandidate{name: name, description: description, aliasOf: target})
}

// checkAlias enforces Alias' registration-time contract.
func checkAlias(name, target string) error {
	switch {
	case name == "":
		return fmt.Errorf("Alias: name must not be empty")
	case target == "":
		return fmt.Errorf("Alias %q: target must not be empty", name)
	case target == name:
		return fmt.Errorf("Alias %q: must not target itself", name)
	}
	if c, ok := findCandidate(target); ok && slices.Contains(c.members, name) {
		return fmt.Errorf("Alias %q: aggregate %q lists it as a member, so the aggregate would include itself", name, target)
	}
	return nil
}

// resolveAlias returns the real task that name records. For a name that is
// not an alias (including an unknown name, which recordSingleTaskBody reports
// with its usual error) it returns name itself and isAlias=false. Aliases
// resolve exactly one level: an alias of an alias is refused rather than
// followed, which keeps every alias's meaning visible at its registration and
// rules out alias-only cycles by construction.
func resolveAlias(name string) (target string, isAlias bool, err error) {
	c, ok := findCandidate(name)
	if !ok || c.aliasOf == "" {
		return name, false, nil
	}
	t, ok := findCandidate(c.aliasOf)
	if !ok {
		return "", true, fmt.Errorf("alias %q targets unknown task %q", name, c.aliasOf)
	}
	if t.aliasOf != "" {
		return "", true, fmt.Errorf("alias %q targets alias %q; an alias must name a real task (here %q)",
			name, c.aliasOf, t.aliasOf)
	}
	return c.aliasOf, true, nil
}
