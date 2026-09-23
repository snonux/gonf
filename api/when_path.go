package api

import (
	"fmt"
	"os"

	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
)

// WhenPathExists runs fn when path exists on the local host. In plan-record
// mode it emits when_begin(path_exists)/when_end around fn instead of probing
// the controller — matching the home_agents early-return pattern as a recipe.
func WhenPathExists(path string, fn func()) {
	if fn == nil {
		return
	}
	p := Expand(path)
	if resource.PlanDraftRecording() {
		plan.Record(plan.Op{
			Op:  plan.KindWhenBegin,
			ID:  fmt.Sprintf("when.path_exists:%s", p),
			All: []plan.Predicate{{PathExists: p}},
		})
		// Each when-fragment is its own recipe scope (see WhenHostname).
		resource.ResetRepository()
		fn()
		plan.Record(plan.Op{Op: plan.KindWhenEnd})
		return
	}
	if _, err := os.Stat(p); err != nil {
		return
	}
	// See WhenHostname's non-recording branch: no per-fragment op scope to
	// isolate here, so a reset before fn() only ever discarded whatever
	// the recipe had registered earlier at top level (task kd2). Only one
	// WhenPathExists branch's fn() ever runs directly at all, so there is
	// no cross-branch ID collision to guard against either.
	fn()
}
