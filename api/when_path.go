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
		fn()
		plan.Record(plan.Op{Op: plan.KindWhenEnd})
		return
	}
	if _, err := os.Stat(p); err != nil {
		return
	}
	fn()
}
