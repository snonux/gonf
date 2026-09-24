package api

import (
	"strings"
	"sync"

	"github.com/snonux/gonf/plan"
)

// Destination guards (task 8h2).
//
// A serializable task guard (WhenLinux, WhenProfile, WhenHostnameContains)
// is a question about the destination, so the controller does not answer it
// when deciding which tasks exist: the task is activated, listed and picked
// up by pattern aggregates everywhere, and its ops are recorded inside a
// when_begin that each destination evaluates at apply time ("record once,
// evaluate per destination"). Before task 8h2 the guard also filtered
// activation on the controller, so `gonf push host home` silently dropped
// every member whose guard only matched the destination.
//
// The controller evaluates such a guard in exactly two places, both with
// the plan engine's own matching rules (plan.EvalPredicates):
//   - activation marks a task whose guard does not hold on this host
//     (TaskInfo.DestinationGuard), so -list shows it as destination-guarded
//     instead of hiding it;
//   - a local Run, whose destination IS this host, resolves the guard of
//     every aggregate member at record time (skippedOnLocalDestination), so
//     the recorded plan is exactly the one earlier releases recorded. Moving
//     that decision into a when_begin would give the same resources the
//     same outcome, but a Privileged member that cannot apply here would
//     still split off an elevated chunk: a sudo/doas re-exec that changes
//     nothing (a password prompt, an extra summary), or a refusal under
//     -privilege none.

// localDestination holds the facts of the destination while a recording is
// known to apply on this very host (Run's local path), and nil otherwise:
// gonf plan, RecordPlan and every push record for a destination the
// controller cannot inspect. Like hostSelection it is process-global state
// scoped to one recording, and recordings are single-goroutine by the DSL
// invariant (recSession); the mutex is purely defensive.
var (
	localDestinationMu sync.Mutex
	localDestination   *Facts
)

// setLocalDestination installs facts as the local destination for the
// duration of one recording and returns the function restoring the previous
// value; the caller defers it so every return path, a panic included,
// restores it.
func setLocalDestination(facts Facts) (restore func()) {
	localDestinationMu.Lock()
	defer localDestinationMu.Unlock()
	prev := localDestination
	localDestination = &facts
	return func() {
		localDestinationMu.Lock()
		defer localDestinationMu.Unlock()
		localDestination = prev
	}
}

// skippedOnLocalDestination reports whether an aggregate must skip member
// name because the current recording applies on this host and the member's
// serializable guard (an alias's: its target's) does not hold here. It
// returns the rendered guard for the debug line. Outside a local recording
// nothing is skipped: the guard travels as a when_begin instead. An unknown
// name or a broken alias is not skipped here; the caller reports those.
func skippedOnLocalDestination(name string) (guard string, skip bool) {
	localDestinationMu.Lock()
	facts := localDestination
	localDestinationMu.Unlock()
	if facts == nil {
		return "", false
	}
	target, _, err := resolveAlias(name)
	if err != nil {
		return "", false
	}
	c, ok := findCandidate(target)
	if !ok {
		return "", false
	}
	guard = unmetGuard(c.planWhen, *facts)
	return guard, guard != ""
}

// unmetGuard returns preds rendered for humans (formatGuard) when they do not
// all hold for facts, and "" when they hold or there are none. Task guards
// only carry host facts, so plan.EvalPredicates cannot fail for them; an
// error would mean a predicate this host cannot evaluate, which counts as
// not holding.
func unmetGuard(preds []plan.Predicate, facts Facts) string {
	if len(preds) == 0 {
		return ""
	}
	if ok, err := plan.EvalPredicates(preds, toPlanFacts(facts)); err == nil && ok {
		return ""
	}
	return formatGuard(preds)
}

// formatGuard renders a conjunctive guard as "fact=value" terms joined by
// " && ", an In list as "fact=a|b", e.g. "goos=linux && profile=fedora|rocky".
func formatGuard(preds []plan.Predicate) string {
	terms := make([]string, 0, len(preds))
	for _, p := range preds {
		switch {
		case p.PathExists != "":
			terms = append(terms, "path_exists="+p.PathExists)
		case len(p.In) > 0:
			terms = append(terms, p.Fact+"="+strings.Join(p.In, "|"))
		default:
			terms = append(terms, p.Fact+"="+p.Eq)
		}
	}
	return strings.Join(terms, " && ")
}

// toPlanFacts converts the DSL's Facts into the plan engine's.
func toPlanFacts(f Facts) plan.Facts {
	return plan.Facts{GOOS: f.GOOS, Profile: f.Profile, Hostname: f.Hostname}
}
