package api

import (
	"fmt"
	"strings"

	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
)

// WhenHostname runs fn when the local hostname contains substr (case
// insensitive; an empty substr always matches). It is the body-level
// counterpart of the WhenHostnameContains TaskOption: in plan-record mode it
// emits when_begin(hostname_contains)/when_end around fn instead of probing
// the controller — so one recorded plan can carry several host-gated
// fragments, each evaluated on the destination at apply time.
//
// Pass List(...) to expand into one fragment per entry (same as looping
// WhenHostname yourself), so identical per-host bodies stay DRY:
//
//	WhenHostname(List("pi2", "pi3"), func() { Package("ksh") })
func WhenHostname[T Path](hosts T, fn func()) {
	if fn == nil {
		return
	}
	switch v := any(hosts).(type) {
	case string:
		whenHostnameOne(v, fn)
	case []string:
		for _, substr := range v {
			whenHostnameOne(substr, fn)
		}
	default:
		panic("unreachable: WhenHostname: Path is string or []string") // see Path
	}
}

func whenHostnameOne(substr string, fn func()) {
	if resource.PlanDraftRecording() {
		plan.Record(plan.Op{
			Op:  plan.KindWhenBegin,
			ID:  fmt.Sprintf("when.hostname:%s", substr),
			All: []plan.Predicate{{Fact: "hostname_contains", Eq: substr}},
		})
		// Each when-fragment is its own recipe scope: the same resource IDs
		// may legitimately be re-declared per host fragment (e.g. one cron
		// task carrying every host's schedule), mirroring the per-task-body
		// reset in RecordPlan.
		resource.ResetRepository()
		fn()
		plan.Record(plan.Op{Op: plan.KindWhenEnd})
		return
	}
	if !strings.Contains(strings.ToLower(DetectFacts().Hostname), strings.ToLower(substr)) {
		return
	}
	// Unlike the recording branch above, this direct (non-recording) path
	// has no per-fragment op scope to isolate — there is nothing here that
	// extracts fn()'s registrations before some later fragment's body
	// runs, so a reset before fn() only ever discarded whatever the
	// recipe had registered earlier at top level, with nothing to show
	// for it (task kd2). Only one WhenHostname branch's fn() ever runs
	// directly at all (the condition is evaluated once, for this one
	// local host), so — unlike recording mode, which must consider every
	// branch without knowing the destination host yet — two branches'
	// resources can never collide here either; there was never a reason
	// to reset.
	fn()
}
