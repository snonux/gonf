package api

import (
	"runtime"

	"github.com/snonux/gonf/internal/declerr"
	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
)

// requireGOOS declares that the resources fn registers only make sense on a
// destination whose GOOS is goos. In plan-record mode it wraps fn in a plan
// requirement block — a when_begin(goos == goos) carrying require (schema 20)
// — so the destination's plan engine refuses the whole apply, dry run
// included, before any mutation when its GOOS differs. Unlike WhenHostname it
// does not reset the repository: the block is a guard around resources of
// the surrounding recipe scope, not a separate per-host fragment, so later
// resources may still depend on or watch what fn registers. The block may
// only be nested under host-fact conditions (WhenHostname, ForHosts, task
// When* facts); under WhenPathExists the recorded plan is refused, because a
// filesystem condition could change during the apply (see plan/require.go).
//
// Without plan recording (resources registered and applied on the same host)
// the check runs immediately against runtime.GOOS: on a mismatch it reports a
// declaration error (internal/declerr, which refuses Apply and the CLI) and
// fn is not run, so nothing of it is registered.
func requireGOOS(goos, id, requirement string, fn func()) {
	if resource.PlanDraftRecording() {
		plan.Record(plan.Op{
			Op:      plan.KindWhenBegin,
			ID:      "when.require_goos:" + goos + ":" + id,
			All:     []plan.Predicate{{Fact: "goos", Eq: goos}},
			Require: id + ": " + requirement,
		})
		fn()
		plan.Record(plan.Op{Op: plan.KindWhenEnd})
		return
	}
	if runtime.GOOS != goos {
		declerr.Reportf("%s: requirement not met on this host (goos=%s): %s", id, runtime.GOOS, requirement)
		return
	}
	fn()
}
