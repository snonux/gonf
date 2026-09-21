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
// remote.PushChunks call plan.ValidateChunks directly and keep the plan
// engine's own wording, because their input is an already-recorded plan (ops
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
