package plan_test

import (
	"context"
	"testing"

	"github.com/snonux/gonf/internal/exec"
	"github.com/snonux/gonf/internal/runners"
	"github.com/snonux/gonf/plan"
)

// TestApplyWithContextInjectsPerRunRunnersNotGlobal is task qb2's
// self-review demonstration for its first migrated kind (resource/cmd,
// the "command" plan kind): two independent ApplyContexts, each carrying
// its own *runners.Set (internal/runners.WithSet), built BEFORE either
// apply runs, converge two different Command ops without either fake ever
// observing the other's invocation.
//
// This is exactly the property task 082's own review flagged as missing:
// internal/testseam's fakes are one process-global slot per kind, installed
// with Fake* and unwound by a registered cleanup — two "sessions" can only
// ever run one after the other, in installation order, and there is no way
// to pre-build two independent configurations and pick one at call time.
// ctxA and ctxB here are built up front, in the opposite order they are
// applied in, which a package-global slot cannot express at all (installing
// setB would still be in effect for anything that runs next, regardless of
// which context object a caller happens to be holding).
//
// This test cannot compile against the pre-qb2 code: plan.ApplyContext had
// no Runners field, internal/runners did not exist, and resource/cmd's Cmd
// had no injectable runFn/probeFn (only a package-level runWith/runProbe
// consulting internal/testseam). qb2's revert-and-retest self-review
// confirmed exactly that: `git apply -R` the qb2 patch, then this file
// failed to compile (undefined: runners, ApplyContext.Runners) while the
// OLD testseam-based resource/cmd tests still passed unmodified.
func TestApplyWithContextInjectsPerRunRunnersNotGlobal(t *testing.T) {
	var aRan, bRan int
	setA := &runners.Set{Command: &runners.CommandRunners{
		Run: func(exec.Opts, string, ...string) (string, string, int, error) {
			aRan++
			return "", "", 0, nil
		},
	}}
	setB := &runners.Set{Command: &runners.CommandRunners{
		Run: func(exec.Opts, string, ...string) (string, string, int, error) {
			bRan++
			return "", "", 0, nil
		},
	}}
	// Both contexts are fully built before either apply runs.
	ctxA := runners.WithSet(context.Background(), setA)
	ctxB := runners.WithSet(context.Background(), setB)

	opsA := []plan.Op{runnersHeader("runners-a"), {Op: plan.KindCommand, ID: "Command[marker-a]", Payload: plan.CommandPayload{Bin: "marker-a"}}}
	opsB := []plan.Op{runnersHeader("runners-b"), {Op: plan.KindCommand, ID: "Command[marker-b]", Payload: plan.CommandPayload{Bin: "marker-b"}}}

	// Apply B first (the reverse of construction order): only setB's fake
	// may run.
	if err := plan.ApplyWithContext(ctxB, opsB, plan.Facts{}, ""); err != nil {
		t.Fatalf("apply B: %v", err)
	}
	if aRan != 0 || bRan != 1 {
		t.Fatalf("after apply B: aRan=%d bRan=%d, want 0 and 1", aRan, bRan)
	}

	// Apply A second: only setA's fake may run; bRan must not move, proving
	// ctxB's runners did not leak into this call.
	if err := plan.ApplyWithContext(ctxA, opsA, plan.Facts{}, ""); err != nil {
		t.Fatalf("apply A: %v", err)
	}
	if aRan != 1 || bRan != 1 {
		t.Fatalf("after apply A: aRan=%d bRan=%d, want 1 and 1 (no leak from ctxB)", aRan, bRan)
	}

	// A third, real apply with no injected runners (a plain
	// context.Background(), no WithSet) must fail on a binary that does not
	// exist rather than silently reuse either fake — the strongest proof
	// neither leaked into an uninjected context.
	opsC := []plan.Op{runnersHeader("runners-c"), {Op: plan.KindCommand, ID: "Command[gonf-qb2-no-such-binary-marker]", Payload: plan.CommandPayload{Bin: "gonf-qb2-no-such-binary-marker"}}}
	if err := plan.Apply(opsC, plan.Facts{}, ""); err == nil {
		t.Fatal("apply C: want an error starting a nonexistent real binary, got nil")
	}
	if aRan != 1 || bRan != 1 {
		t.Fatalf("after apply C: aRan=%d bRan=%d, want unchanged 1 and 1", aRan, bRan)
	}
}

func runnersHeader(id string) plan.Op {
	return plan.Op{Op: plan.KindPlan, Version: plan.CurrentVersion, ID: id}
}
