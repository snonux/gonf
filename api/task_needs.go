package api

import (
	"fmt"
	"slices"
	"strings"

	"github.com/snonux/gonf/internal/logger"
)

// Needs declares that the task requires other tasks: wherever the task is
// recorded (Run, the CLI, gonf plan, push/cluster/fleet, an aggregate), its
// needed tasks are recorded right before it, unless the same Run list or
// aggregate tree already recorded them:
//
//	Task("web", "", web, Needs("pf", "base"))
//	Run("web")        // records pf, base, web
//	Run("base", "web") // records base, pf, web
//
// Resolution: in RegisterMethods(..., WithPrefix("frontends_")) a name is
// tried relative to the prefix first ("pf" → "frontends_pf"), then as a full
// name; outside RegisterMethods it is a full name. Aliases resolve to their
// target. Names are resolved when a plan is recorded, so the needed task may
// be registered after its dependent.
//
// Semantics (the simple, safe design; auto-inclusion instead of refusal):
//   - Each needed task records with its own guards and privilege, in the
//     envelope the dependent records in, never inside the dependent's own
//     when_begin: Needs never widens or narrows where the needed task
//     applies.
//   - Deduplication is by scope, exactly like an aggregate: a need already
//     recorded earlier in the same Run(...) list or aggregate tree is not
//     recorded again, and a later explicit name in the same scope that a
//     Needs already recorded is skipped. A task body running Run(...) starts
//     a scope of its own, since its envelope may differ.
//   - A plan without Needs records exactly as before.
//   - A task that needs Operational work counts as operational for pattern
//     aggregates (containsOperational), so a pattern cannot pull it in.
//
// Errors: an empty name, a task that needs itself, or a Needs cycle
// (a → b → a, aliases followed) is a declaration error at registration and
// the task is not queued. A need naming no registered task fails the record
// that reaches it, like an unknown AggregateTasks member; so does a need
// whose opaque When fails on the controller.
func Needs(tasks ...string) TaskOption {
	return func(c *taskCandidate) { c.needs = append(c.needs, tasks...) }
}

// needsPrefix sets the RegisterMethods prefix relative Needs names resolve
// against (resolveNeedName). Internal: RegisterMethods adds it.
func needsPrefix(prefix string) TaskOption {
	return func(c *taskCandidate) { c.needsPrefix = prefix }
}

// resolveNeedName returns the task a Needs entry n names: prefix+n when that
// exists, else n itself when that exists.
func resolveNeedName(prefix, n string, exists func(string) bool) (string, bool) {
	if prefix != "" && exists(prefix+n) {
		return prefix + n, true
	}
	if exists(n) {
		return n, true
	}
	return "", false
}

// candidateExists reports whether a task, aggregate or alias named name is
// queued.
func candidateExists(name string) bool {
	_, ok := findCandidate(name)
	return ok
}

// resolvedNeeds returns c's needs resolved against the registry, skipping
// the ones that name nothing yet (recording reports those).
func (c taskCandidate) resolvedNeeds() []string {
	return c.needEdges(candidateExists)
}

// needEdges is the Needs graph edge list of c for a registry view exists:
// an alias leads to its target, any other task to its resolved needs.
func (c taskCandidate) needEdges(exists func(string) bool) []string {
	if c.aliasOf != "" {
		return []string{c.aliasOf}
	}
	var out []string
	for _, n := range c.needs {
		if name, ok := resolveNeedName(c.needsPrefix, n, exists); ok {
			out = append(out, name)
		}
	}
	return out
}

// checkNeeds enforces Needs' registration-time contract on the candidate c
// that is about to be queued: no empty name, no self need, no cycle.
func checkNeeds(c taskCandidate) error {
	if len(c.needs) == 0 {
		return nil
	}
	exists := func(name string) bool { return name == c.name || candidateExists(name) }
	for _, n := range c.needs {
		if n == "" {
			return fmt.Errorf("Task %q: Needs: task name must not be empty", c.name)
		}
		if name, ok := resolveNeedName(c.needsPrefix, n, exists); ok && name == c.name {
			return fmt.Errorf("Task %q: Needs: a task must not need itself", c.name)
		}
	}
	if chain := needsCycle(c, exists); chain != nil {
		return fmt.Errorf("Task %q: Needs cycle: %s", c.name, strings.Join(chain, " -> "))
	}
	return nil
}

// needsCycle returns the chain c -> ... -> c when following Needs (and
// alias) edges from c leads back to c, or nil. Checking at every
// registration catches every cycle: the task that closes one is registered
// last, when all the others are already queued.
func needsCycle(c taskCandidate, exists func(string) bool) []string {
	lookup := func(name string) (taskCandidate, bool) {
		if name == c.name {
			return c, true
		}
		return findCandidate(name)
	}
	visited := map[string]bool{}
	var walk func(cur taskCandidate, path []string) []string
	walk = func(cur taskCandidate, path []string) []string {
		for _, next := range cur.needEdges(exists) {
			if next == c.name {
				return append(slices.Clone(path), next)
			}
			nc, ok := lookup(next)
			if !ok || visited[next] {
				continue
			}
			visited[next] = true
			if chain := walk(nc, append(slices.Clone(path), next)); chain != nil {
				return chain
			}
		}
		return nil
	}
	return walk(c, []string{c.name})
}

// needsScope is the Needs dedupe scope of one Run(...) list
// (recSession.needs): recorded holds every task (alias target) the list or
// an aggregate tree under it recorded, viaNeeds the ones only a Needs
// recorded, which a later explicit name in the list then skips.
type needsScope struct {
	recorded map[string]bool
	viaNeeds map[string]bool
}

// enterNeedsScope installs a fresh Needs scope for one Run(...) list and
// returns the function restoring the previous one.
func enterNeedsScope() (restore func()) {
	saved := recSession.needs
	recSession.needs = &needsScope{recorded: map[string]bool{}, viaNeeds: map[string]bool{}}
	return func() { recSession.needs = saved }
}

// recordNeeds records the needs of task name that its scope has not
// recorded yet, in declaration order, each through recordTaskName (so a
// need's own needs, alias, guard, privilege and cycle check all apply).
func recordNeeds(name string) error {
	c, ok := findCandidate(name)
	if !ok || len(c.needs) == 0 {
		return nil
	}
	for _, n := range c.needs {
		need, ok := resolveNeedName(c.needsPrefix, n, candidateExists)
		if !ok {
			return fmt.Errorf("task %q needs unknown task %q", name, n)
		}
		target, _, err := resolveAlias(need)
		if err != nil {
			return fmt.Errorf("task %q needs %q: %w", name, need, err)
		}
		if needRecorded(target) {
			continue
		}
		if err := recordTaskName(need); err != nil {
			return err
		}
		noteNeedRecorded(target)
	}
	return nil
}

// needRecorded reports whether target was already recorded in the current
// scope: the aggregate tree, or the Run(...) list.
func needRecorded(target string) bool {
	if recSession.aggregateSeen[target] {
		return true
	}
	return recSession.needs != nil && recSession.needs.recorded[target]
}

// noteRecorded marks name's target as recorded in the current Run(...)
// list, so a later Needs of it is satisfied. It never causes a skip on its
// own (only viaNeeds does), so a plan without Needs is unchanged.
func noteRecorded(name string) {
	if recSession.needs == nil {
		return
	}
	if target, _, err := resolveAlias(name); err == nil {
		recSession.needs.recorded[target] = true
	}
}

// noteNeedRecorded marks target as recorded by a Needs, in the aggregate
// tree (so the aggregate skips it as a later member) and in the list.
func noteNeedRecorded(target string) {
	if recSession.aggregateSeen != nil {
		recSession.aggregateSeen[target] = true
	}
	if recSession.needs != nil {
		recSession.needs.recorded[target] = true
		recSession.needs.viaNeeds[target] = true
	}
}

// skipNeeded reports whether an explicit name of the current Run(...) list
// must be skipped because a Needs earlier in the list already recorded it.
func skipNeeded(name string) bool {
	if recSession.needs == nil || len(recSession.needs.viaNeeds) == 0 {
		return false
	}
	target, _, err := resolveAlias(name)
	if err != nil || !recSession.needs.viaNeeds[target] {
		return false
	}
	logger.Debug("Run: skipping %q: a Needs earlier in the list already recorded it", name)
	return true
}
