package api

import (
	"errors"
	"fmt"

	"github.com/snonux/gonf/plan"
)

// Fix hints appended to a re-worded dangling-reference refusal. They differ by
// entry point because the remedy differs: an Apply caller registers resources
// itself, a recorded task set registers them from task bodies.
const (
	fixHintApply  = "register that resource before calling Apply"
	fixHintRecord = "make sure a task in this plan registers that resource"
)

// refusedError is a plan pre-flight refusal in the vocabulary of the api
// caller. It is not a fmt.Errorf("%w") wrapper on purpose: wrapping would put
// the plan engine's own wording ("plan: op ...") into the message again and
// double the prefix ("Apply: plan: ..."). Instead the message is composed once
// (msg) while Unwrap keeps the typed plan error reachable, so callers can still
// errors.As a *plan.DanglingDepError, *plan.DanglingWatchError or plan.Refusal.
type refusedError struct {
	msg   string
	cause error
}

func (e *refusedError) Error() string { return e.msg }
func (e *refusedError) Unwrap() error { return e.cause }

// preflightChunks is the single api-side wrapper around plan.ValidateChunks,
// used by every api entry point that holds a whole plan: RecordPlanTo
// (caller "RecordPlan"), Apply (caller "Apply"). ApplyChunks and
// remote.Delivery.ToHost (every push and strict preview, single target or
// fan-out) call plan.ValidateChunks directly and keep the plan engine's own
// wording, because their input is an already-recorded plan (ops
// from a file or a previous record), not registered resources.
//
// The refusal is re-worded as "<caller>: <reason>" with exactly one prefix.
// Dangling dependency and watch IDs are re-phrased in terms of registered
// resources (what api callers know); every other refusal (forward cross-chunk
// dependency, cross-chunk watch, gate without a watch, requirement block with
// a non-host-fact scope) keeps the engine's reason text minus its "plan: "
// prefix.
func preflightChunks(caller, fixHint string, chunks []plan.Chunk) error {
	err := plan.ValidateChunks(chunks)
	if err == nil {
		return nil
	}
	return &refusedError{msg: caller + ": " + refusalReason(err, fixHint), cause: err}
}

// refusalReason renders err (a plan.ValidateChunks result) without any prefix.
func refusalReason(err error, fixHint string) string {
	var dep *plan.DanglingDepError
	var watch *plan.DanglingWatchError
	var refusal plan.Refusal
	switch {
	case errors.As(err, &dep):
		return fmt.Sprintf(
			"%s depends on %s, which is not a registered resource (dangling dependency); "+
				"check the spelling of the ID passed to DependsOn and %s",
			dep.Op, dep.Dep, fixHint)
	case errors.As(err, &watch):
		return fmt.Sprintf(
			"%s watches %s, which is not a registered resource (dangling watch); "+
				"check the spelling of the ID passed to OnChange/WatchChanges and %s",
			watch.Op, watch.Watch, fixHint)
	case errors.As(err, &refusal):
		return refusal.Reason()
	default:
		return err.Error()
	}
}

// crossChunkWatchRefusal words the change-gate refusal for a watch crossing a
// privilege chunk in the terms of an Apply caller, who registered resources
// and never chose chunks: "Command[g] (unprivileged) watches Command[e]
// (elevated); ...". It reports exactly the violation plan.ValidateChunks would
// report, and keeps that plan refusal as the cause (errors.As still finds a
// plan.Refusal). It returns nil, leaving preflightChunks to word the plan's
// first problem, when that problem is not a cross-chunk watch: a dependency
// refusal (checked first by plan.ValidateChunks), or a gate without a watch
// or with a dangling one ahead of any cross-chunk watch.
func crossChunkWatchRefusal(caller string, chunks []plan.Chunk) error {
	bodies := make([][]plan.Op, len(chunks))
	chunkOf := map[string]int{}
	for i, ch := range chunks {
		bodies[i] = ch.Ops
		for _, op := range ch.Ops {
			if _, seen := chunkOf[op.ID]; op.ID != "" && !plan.IsControlKind(op.Op) && !seen {
				chunkOf[op.ID] = i
			}
		}
	}
	if plan.ValidateChunkDeps(bodies) != nil {
		return nil
	}
	for i, ch := range chunks {
		for _, op := range ch.Ops {
			if !op.IfChanged || len(op.Watch) == 0 {
				continue // an empty watch is refused by preflightChunks
			}
			for _, w := range op.Watch {
				j, ok := chunkOf[w]
				if !ok {
					return nil // dangling: preflightChunks words it
				}
				if j != i {
					return &refusedError{
						msg:   caller + ": " + watchAcrossChunks(op.ID, ch.Elevate, w, chunks[j].Elevate),
						cause: plan.ValidateChangeGates(bodies),
					}
				}
			}
		}
	}
	return nil
}

// watchAcrossChunks explains why gated cannot watch watched: across privilege
// classes, or within one class forced into separate chunks by dependencies
// on the other class. Change reports are chunk-local either way.
func watchAcrossChunks(gated string, gatedElevate bool, watched string, watchedElevate bool) string {
	head := fmt.Sprintf("%s (%s) watches %s (%s)", gated, privilegeClass(gatedElevate), watched, privilegeClass(watchedElevate))
	if gatedElevate != watchedElevate {
		return head + "; change reports are not carried across privilege classes (the elevated " +
			"resources apply in a separate process), so a change-gated resource can only watch " +
			"resources of its own class: gate on a resource of the same class, or elevate both or neither"
	}
	return head + ", but their dependencies need resources of the other privilege class applied " +
		"between the two, so they cannot share a privilege chunk and change reports are not carried " +
		"across chunks: remove that dependency path or the change gate"
}
