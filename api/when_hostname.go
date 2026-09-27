package api

import (
	"fmt"
	"strings"

	"github.com/snonux/gonf/internal/declerr"
	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
)

// WhenHostname runs fn when the local hostname contains substr (case
// insensitive). It is the body-level
// counterpart of the WhenHostnameContains TaskOption: in plan-record mode it
// emits when_begin(hostname_contains)/when_end around fn instead of probing
// the controller — so one recorded plan can carry several host-gated
// fragments, each evaluated on the destination at apply time.
//
// Pass List(...) to expand into one fragment per entry (same as looping
// WhenHostname yourself), so identical per-host bodies stay DRY:
//
//	WhenHostname(List("pi2", "pi3"), func() { Package("ksh") })
//
// An empty or whitespace-only fragment (e.g. an unset config value) is a
// declaration error, the same check WhenHostnameIn/WhenHostnameContains
// apply (checkBlankHostnameFragments): every hostname contains "", so it
// would run fn on every destination. The whole call is then skipped — no
// fragment of it records or runs — so a List with one blank entry never
// half-applies. An empty List() has no fragments and runs nothing.
func WhenHostname[T Path](hosts T, fn func()) {
	if fn == nil {
		return
	}
	var substrs []string
	switch v := any(hosts).(type) {
	case string:
		substrs = []string{v}
	case []string:
		substrs = v
	default:
		panic("unreachable: WhenHostname: Path is string or []string") // see Path
	}
	if err := checkBlankHostnameFragments("WhenHostname", substrs); err != nil {
		declerr.Report(err)
		return
	}
	for _, substr := range substrs {
		whenHostnameOne(substr, fn)
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
	// for it (task kd2). Removing that reset does NOT mean collisions are
	// impossible here, though: each WhenHostname call is an independent
	// condition, and several can match this one local host at once
	// (overlapping substrings, or several List(...) entries that both
	// match), so their fn() bodies run into the SAME repository in turn.
	// Direct apply therefore requires resource IDs to stay unique across
	// every fragment that matches this host — unlike the recording branch
	// above, which deliberately keeps each fragment in its own scope (an
	// earlier version of this comment wrongly claimed a collision could
	// never happen here; task vd2 corrected it after a probe reproduced
	// exactly that with two WhenPathExists fragments). Name this
	// condition while fn() runs so a same-ID collision names both
	// colliding When*/WhenPathExists calls instead of a bare "already
	// registered" (resource.collisionError, via resource.Register's
	// declerr.Reportf).
	pop := resource.PushWhenContext(fmt.Sprintf("WhenHostname(%q)", substr))
	defer pop()
	fn()
}
