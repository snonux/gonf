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
// Pass a []string to expand into one fragment per entry (same as looping
// WhenHostname yourself), so identical per-host bodies stay DRY:
//
//	WhenHostname([]string{"pi2", "pi3"}, func() { Package("ksh") })
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
		panic("WhenHostname: hosts must be string or []string")
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
	resource.ResetRepository()
	fn()
}
