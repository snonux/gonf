package api

import (
	"fmt"

	"github.com/snonux/gonf/internal/declerr"
	"github.com/snonux/gonf/internal/logger"
)

// Aggregate registers a task that runs every activated task whose name matches
// pattern (via Matching), in sorted name order.
//
// Membership rules, applied when the aggregate is recorded:
//   - a member's serializable guard (WhenLinux, WhenProfile,
//     WhenHostnameContains) does not decide membership: the member is
//     recorded inside its when_begin and each destination decides (task
//     8h2), so `gonf push host home` includes a member whose guard only
//     matches host. Only an opaque When predicate failing on the controller
//     leaves a task out (it is not activated). A local Run is the one
//     exception: its destination is this host, so it skips a member whose
//     guard does not hold here at record time (skippedOnLocalDestination);
//   - the aggregate itself is excluded, whether matched by its own name or
//     through an Alias of it (a pattern such as ".*" would otherwise recurse
//     into itself);
//   - operational work is never included: an Operational task, an Alias of
//     one, or an AggregateTasks that transitively lists one (a pattern
//     aggregate is itself free of operational work by the same rule);
//   - an Alias is recorded as its target, and a target is recorded at most
//     once per aggregate tree (see recordAggregate).
//
// For a setup aggregate whose membership is a safety decision, prefer
// AggregateTasks and list the members instead of growing a regex.
//
// Error handling: a Task fn cannot return errors, so a failure inside the
// body — the pattern matching no tasks, or a child task failing to record —
// is stashed (stashAggregateError) and fails the surrounding RecordPlan
// session with a returned error naming the aggregate and the underlying child
// error. Nothing is applied in that case: the abort happens during recording,
// before plan apply runs. This mirrors the plan recorder's cycle-stash
// mechanism and keeps Run's deferred temp-dir cleanup intact (no process
// exit).
func Aggregate(name, description, pattern string) {
	Task(name, description, func() {
		recordAggregate(name, patternMembers(name, pattern),
			fmt.Sprintf("pattern %q matched no tasks (after excluding itself and operational tasks)", pattern))
	}, asAggregate(nil))
}

// AggregateTasks registers a task that runs the listed member tasks in the
// given order — explicit membership for setup aggregates, so which tasks run
// (and which operational actions stay out) is visible in one list instead of
// being encoded in a regex. Members may be tasks, other aggregates, or
// aliases; an alias is recorded as its target and a target is recorded at
// most once per aggregate tree (see recordAggregate). An Operational member
// is included, because listing it is explicit — but that makes this
// aggregate operational work for pattern aggregates, which then skip it.
//
// Members follow the same condition rules as a pattern Aggregate: a member
// whose opaque When predicate excludes it on the controller is skipped, one
// with only a serializable guard is recorded inside its when_begin (a local
// Run skips it when the guard does not hold on this host); a member name
// that is not registered at all, or a broken alias, fails the record (a
// typo must not shrink a setup run silently), as does a list whose members
// are all skipped. The list itself is checked at registration: no members,
// an empty member name, a duplicate member, or a member that is the
// aggregate itself — by name or through an already registered Alias — is
// reported as a declaration error (internal/declerr) and the aggregate is
// not registered. Alias applies the same check from the other side, so
// registration order does not matter.
func AggregateTasks(name, description string, members ...string) {
	if err := checkAggregateMembers(name, members); err != nil {
		declerr.Report(err)
		return
	}
	list := append([]string(nil), members...)
	Task(name, description, func() {
		names, err := activeMembers(name, list)
		if err != nil {
			stashAggregateError(name, err)
			return
		}
		recordAggregate(name, names, "no member task is active for these facts")
	}, asAggregate(list))
}

// asAggregate marks a candidate as an aggregate (members is nil for a
// pattern Aggregate). It is internal: only the two constructors set it.
func asAggregate(members []string) TaskOption {
	return func(c *taskCandidate) {
		c.aggregate = true
		c.members = members
	}
}

// checkAggregateMembers enforces AggregateTasks' registration-time contract.
func checkAggregateMembers(name string, members []string) error {
	if len(members) == 0 {
		return fmt.Errorf("AggregateTasks %q: at least one member task is required", name)
	}
	seen := make(map[string]bool, len(members))
	for _, m := range members {
		switch {
		case m == "":
			return fmt.Errorf("AggregateTasks %q: member name must not be empty", name)
		case m == name:
			return fmt.Errorf("AggregateTasks %q: must not list itself as a member", name)
		case seen[m]:
			return fmt.Errorf("AggregateTasks %q: member %q listed twice", name, m)
		}
		if c, ok := findCandidate(m); ok && c.aliasOf == name {
			return fmt.Errorf("AggregateTasks %q: member %q is an alias of the aggregate itself", name, m)
		}
		seen[m] = true
	}
	return nil
}

// patternMembers returns the activated tasks matching pattern, minus the
// aggregate itself (directly or through an alias), all operational work and,
// on a local Run, members whose guard does not hold here (see Aggregate).
func patternMembers(name, pattern string) []string {
	var names []string
	for _, n := range Matching(pattern) {
		if n == name {
			continue
		}
		if t, ok := activeTask(n); ok && t.aliasOf == name {
			continue
		}
		if containsOperational(n, map[string]bool{}) {
			// Dropping an aggregate that merely contains operational work is
			// silent by design; the debug line (-verbose) explains why a
			// matched name did not run.
			logger.Debug("aggregate %s: skipping %q: it is or contains an Operational task", name, n)
			continue
		}
		if skipLocalMember(name, n) {
			continue
		}
		names = append(names, n)
	}
	return names
}

// skipLocalMember reports whether aggregate must skip member n on a local
// Run because n's serializable guard does not hold on this host; the debug
// line (-verbose) explains why a matched name did not run.
func skipLocalMember(aggregate, n string) bool {
	guard, skip := skippedOnLocalDestination(n)
	if skip {
		logger.Debug("aggregate %s: skipping %q: its guard %s does not hold on this host", aggregate, n, guard)
	}
	return skip
}

// containsOperational reports whether recording name could record an
// Operational task: name is operational itself, an alias of operational
// work, an AggregateTasks listing operational work at any depth, or a task
// that Needs operational work (it would record that work first). Every
// listed member counts, active or not, so the answer does not depend on the
// controller's facts. A pattern Aggregate needs no descent: it applies this
// same filter to its own members. visited guards against registration
// cycles, which recording reports separately.
func containsOperational(name string, visited map[string]bool) bool {
	if visited[name] {
		return false
	}
	visited[name] = true
	c, ok := findCandidate(name)
	if !ok {
		return false
	}
	if c.operational {
		return true
	}
	if c.aliasOf != "" {
		return containsOperational(c.aliasOf, visited)
	}
	// A task's Needs record wherever it records, so needed operational
	// work counts as contained, like an AggregateTasks member.
	for _, m := range append(c.resolvedNeeds(), c.members...) {
		if containsOperational(m, visited) {
			return true
		}
	}
	return false
}

// activeMembers returns the members aggregate lists that are active for
// the controller's facts (their opaque predicates hold) and, on a local Run,
// whose guard holds here, in list order. A member with no registration at
// all, or a broken alias (which is never active and so would otherwise be
// skipped quietly), is an error rather than a skip.
func activeMembers(aggregate string, members []string) ([]string, error) {
	var names []string
	for _, m := range members {
		if _, ok := findCandidate(m); !ok {
			return nil, fmt.Errorf("unknown member task %q", m)
		}
		if _, _, err := resolveAlias(m); err != nil {
			return nil, err
		}
		if _, ok := activeTask(m); ok && !skipLocalMember(aggregate, m) {
			names = append(names, m)
		}
	}
	return names, nil
}

// recordAggregate records names in order into the current plan session,
// deduplicating across the whole aggregate tree: a member whose target (an
// alias resolved to the task it names) was already recorded by this
// aggregate or by any aggregate nested in the same tree is skipped, so it is
// recorded once, at its first position. Members are recorded under their
// public names, so an alias stays on the recursion stack and a cycle error
// names it. A target counts as recorded only once its recording finished;
// a member that is still being recorded is a cycle, which the recorder
// reports instead of skipping. Failures are stashed with the aggregate's
// name because a Task body cannot return them; emptyReason explains an empty
// member set.
func recordAggregate(name string, names []string, emptyReason string) {
	if len(names) == 0 {
		stashAggregateError(name, fmt.Errorf("%s", emptyReason))
		return
	}
	if recSession.aggregateSeen == nil {
		recSession.aggregateSeen = map[string]bool{}
		defer func() { recSession.aggregateSeen = nil }()
	}
	for _, n := range names {
		target, _, err := resolveAlias(n)
		if err != nil {
			stashAggregateError(name, err)
			return
		}
		if recSession.aggregateSeen[target] {
			continue
		}
		if err := recordTaskName(n); err != nil {
			stashAggregateError(name, err)
			return
		}
		recSession.aggregateSeen[target] = true
		noteRecorded(target) // a later Needs in the same list is satisfied
	}
}

// enterAggregateScope prepares the aggregate dedupe scope for one task body
// and returns the function restoring the previous scope. An aggregate body
// keeps the enclosing scope, so nested aggregates deduplicate together. Any
// other body gets a fresh (nil) scope: it may carry its own When/Privileged
// envelope, so an aggregate it runs must not skip a member merely because an
// outer aggregate recorded that member under different guards. The Needs
// scope (recSession.needs) follows the same rule for the same reason.
func enterAggregateScope(isAggregate bool) (restore func()) {
	if isAggregate {
		return func() {}
	}
	savedSeen, savedNeeds := recSession.aggregateSeen, recSession.needs
	recSession.aggregateSeen, recSession.needs = nil, nil
	return func() { recSession.aggregateSeen, recSession.needs = savedSeen, savedNeeds }
}

// activeTask returns the activated entry for name, activating the registry
// with DetectFacts first when nothing has activated it yet (as Matching does).
func activeTask(name string) (task, bool) {
	ensureActivated()
	tasksMu.Lock()
	defer tasksMu.Unlock()
	t, ok := tasks[name]
	return t, ok
}
