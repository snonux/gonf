package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/snonux/gonf/api/options"
)

// TestWhenPathExistsDirectCollisionNamesConditions reproduces the exact
// two-WhenPathExists collision task vd2 probed at commit f67f179:
// WhenPathExists("/etc", File(p,"A")); WhenPathExists("/tmp", File(p,"B"))
// then Apply(). Here both conditions are two temp dirs guaranteed to exist,
// so the test does not depend on the host's filesystem layout.
//
// Task kd2 correctly removed the per-fragment resource.ResetRepository()
// call from the non-recording branches of WhenHostname/WhenPathExists (see
// api/when_hostname.go, api/when_path.go) -- but that means a collision
// like this one is a REAL possibility on the direct (non-recording)
// api.Apply path: each WhenPathExists call is an independent condition,
// and several can match the same local host at once, so their bodies run
// their File() declarations into the SAME repository. Unlike the recording
// path (gonf plan/push/Run), which gives every matching fragment its own
// repository scope and so accepts the identical recipe shape, direct apply
// requires resource IDs to stay unique across every fragment that matches
// the host. The refusal must be actionable: it names BOTH colliding
// WhenPathExists conditions, not a bare "resource ... already registered".
func TestWhenPathExistsDirectCollisionNamesConditions(t *testing.T) {
	ResetForTest()
	// Unlike the narrower `lastRecordFailure = nil` cleanup other tests use
	// (for a RecordPlanTo failure, which never reaches declerr.First() --
	// RecordPlanTo captures it into its own session), this test's collision
	// goes straight through resource.Register's declerr.Reportf with no
	// sink installed, so it sets the STICKY process-wide declerr.First()
	// (see AGENTS.md's "Registration-time contract"). A full ResetForTest
	// clears that so later tests in this shuffled binary do not inherit it.
	t.Cleanup(ResetForTest)

	dirA := t.TempDir()
	dirB := t.TempDir()
	out := filepath.Join(t.TempDir(), "out.txt")

	WhenPathExists(dirA, func() { File(out, options.WithContent("A")) })
	WhenPathExists(dirB, func() { File(out, options.WithContent("B")) })

	err := Apply()
	if err == nil {
		t.Fatal("expected a resource-collision error, got nil")
	}
	if !strings.Contains(err.Error(), "already registered") {
		t.Fatalf("error = %q, want it to mention \"already registered\"", err)
	}
	wantA := `WhenPathExists("` + dirA + `")`
	wantB := `WhenPathExists("` + dirB + `")`
	if !strings.Contains(err.Error(), wantA) {
		t.Fatalf("error = %q, want it to name the first colliding condition %q", err, wantA)
	}
	if !strings.Contains(err.Error(), wantB) {
		t.Fatalf("error = %q, want it to name the second colliding condition %q", err, wantB)
	}

	// Nothing must have been written: the collision is refused before
	// anything applies.
	if _, statErr := os.Stat(out); statErr == nil {
		t.Fatalf("%s must not have been written when registration collided", out)
	}
}

// TestWhenPathExistsDirectCollisionPoisonsLaterUnrelatedApply documents the
// deliberate sticky-first-report behaviour of internal/declerr (see
// AGENTS.md's "Registration-time contract": "the first [declaration error]
// report wins ... kept for the process"). Task vd2 does not change that
// mechanism. Once a WhenPathExists collision reports the process's first
// declaration error, a later, WHOLLY UNRELATED registration and Apply call
// is refused with that SAME sticky error -- even though the later call has
// nothing to do with the collision, and even though its own resource was
// registered successfully (no ID clash of its own). What this task fixes
// is that the sticky error itself stays legible when it resurfaces here: it
// still names the two WhenPathExists conditions that actually collided,
// instead of a bare "already registered" an operator could mistake for
// being about the unrelated resource.
func TestWhenPathExistsDirectCollisionPoisonsLaterUnrelatedApply(t *testing.T) {
	ResetForTest()
	// See TestWhenPathExistsDirectCollisionNamesConditions: this test
	// deliberately sets the sticky declerr.First() and must clear it so
	// later tests in this shuffled binary are unaffected.
	t.Cleanup(ResetForTest)

	dirA := t.TempDir()
	dirB := t.TempDir()
	collided := filepath.Join(t.TempDir(), "collided.txt")

	WhenPathExists(dirA, func() { File(collided, options.WithContent("A")) })
	WhenPathExists(dirB, func() { File(collided, options.WithContent("B")) })

	if err := Apply(); err == nil {
		t.Fatal("expected the collision to fail this first Apply")
	}

	// A completely unrelated declaration and Apply call, in the same
	// process, after the collision -- no When* involved, no ID clash of
	// its own.
	unrelated := filepath.Join(t.TempDir(), "unrelated.txt")
	File(unrelated, options.WithContent("q"))

	err := Apply()
	if err == nil {
		t.Fatal("expected the sticky first declaration error to refuse this unrelated Apply too")
	}
	// Check the PRECISE quoted conditions, not just the bare word
	// "WhenPathExists": t.TempDir() names its directory after this test's
	// own name, which itself contains "WhenPathExists" -- a bare substring
	// check would pass trivially on that coincidence even under the OLD,
	// unscoped error text, without actually exercising the fix.
	wantA := `WhenPathExists("` + dirA + `")`
	wantB := `WhenPathExists("` + dirB + `")`
	if !strings.Contains(err.Error(), wantA) || !strings.Contains(err.Error(), wantB) {
		t.Fatalf("sticky refusal lost its own cause, looks unrelated to the operator: %v (want it to still name %q and %q)", err, wantA, wantB)
	}
	if _, statErr := os.Stat(unrelated); statErr == nil {
		t.Fatalf("%s must not have been written: the sticky refusal must still block an unrelated later Apply", unrelated)
	}
}
