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
func WhenHostname(substr string, fn func()) {
	if fn == nil {
		return
	}
	if resource.PlanDraftRecording() {
		plan.Record(plan.Op{
			Op:  plan.KindWhenBegin,
			ID:  fmt.Sprintf("when.hostname:%s", substr),
			All: []plan.Predicate{{Fact: "hostname_contains", Eq: substr}},
		})
		fn()
		plan.Record(plan.Op{Op: plan.KindWhenEnd})
		return
	}
	if !strings.Contains(strings.ToLower(DetectFacts().Hostname), strings.ToLower(substr)) {
		return
	}
	fn()
}
