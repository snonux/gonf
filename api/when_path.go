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
	// the recipe had registered earlier at top level (task kd2). That does
	// NOT make a collision impossible: several WhenPathExists fragments
	// (or a WhenPathExists alongside a WhenHostname) can match this one
	// local host at once (two paths that both exist), so their fn() bodies
	// run into the SAME repository in turn — direct apply requires
	// resource IDs to stay unique across every matching fragment, unlike
	// the recording branch above (task vd2 corrected this comment, which
	// used to wrongly claim cross-branch collisions were impossible here;
	// probed and reproduced with two WhenPathExists fragments). Name this
	// condition while fn() runs so a same-ID collision names both
	// colliding When*/WhenPathExists calls instead of a bare "already
	// registered" (resource.collisionError, via resource.Register's
	// declerr.Reportf).
	pop := resource.PushWhenContext(fmt.Sprintf("WhenPathExists(%q)", p))
	defer pop()
	fn()
}
