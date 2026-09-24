package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/internal/declerr"
	"github.com/snonux/gonf/resource"
)

// These tests pin task hg2: RecordPlanTo now re-checks declerr.First() once
// more right after recordPlanBody returns a nil error, and fails the record
// with whatever it finds there. This closes the WHOLE CLASS of the
// sink-teardown bug tf2/vf2 partially fixed, regardless of which code path
// tore down the active recording's declerr capture sink (api/plan.go's
// enterRecordMode installs declerr.Capture(stashBodyError) for the duration
// of RecordPlanTo):
//
//   - tf2 closed ONE route (resource.ResetDeclarationError called mid-
//     recording) by giving it a narrower first-only clear that never
//     touches the sink (internal/declerr.TakeFirst since task kg2). resource.ResetForTest, by design, still calls the broader
//     internal/declerr.Reset (it must wipe the sink for its own, legitimate
//     between-tests use) — so a task body that mis-calls ResetForTest instead
//     of ResetDeclarationError mid-recording reaches the identical outcome:
//     a later declaration error (e.g. a failed MustSecret) misses the torn-
//     down sink, lands on the process-wide sticky declerr.First() slot
//     instead, and — before hg2 — nothing re-checked that slot once the
//     record otherwise finished cleanly, so a credentials file was silently
//     written with an empty secret and Run returned nil.
//   - hg2 fixes this structurally at the other end instead of patching this
//     route too: see TestResetForTestMidRecordingDoesNotSwallowLaterFailure
//     below for the exact byte-for-byte probe with ResetForTest as the
//     culprit, and TestArbitrarySinkLossMidRecordingDoesNotSwallowLaterFailure
//     for a second, unrelated route (a bare declerr.Reset() call, standing in
//     for any future code with the same effect) — both must now fail loudly,
//     because the fix does not care how the sink was lost.

// TestResetForTestMidRecordingDoesNotSwallowLaterFailure reproduces task
// hg2's confirmed probe: a task body that defensively calls
// resource.ResetForTest() (a test-only seam, never meant to run mid-
// recording, but nothing enforced that) and then goes on to declare a
// resource from a failing MustSecret call. Before hg2's fix this passed
// silently: Run returned nil, the summary reported a change, and the target
// file was written to disk containing the literal "password=" with the
// secret empty. After the fix, RecordPlanTo's post-recordPlanBody re-check
// of declerr.First() must catch the stray report and fail the record loudly.
func TestResetForTestMidRecordingDoesNotSwallowLaterFailure(t *testing.T) {
	ResetForTest()
	useSecretWorkDir(t) // no "secrets" dir here: MustSecret("db/password") fails
	t.Cleanup(ResetForTest)

	target := filepath.Join(t.TempDir(), "creds.txt")
	Task("t", "", func() {
		// The mid-recording misuse task hg2's annotation reproduces: this
		// tears down the active recording's declerr capture sink exactly
		// like the tf2-era ResetDeclarationError call did, via a different
		// route (resource.ResetForTest, not resource.ResetDeclarationError).
		resource.ResetForTest()
		s := MustSecret("db/password")
		File(target, options.WithContent("password="+s))
	})

	err := Run("t")
	if err == nil {
		t.Fatal("Run(t) = nil, want the MustSecret failure to fail the record: " +
			"RecordPlanTo must re-check declerr.First() after recordPlanBody returns (task hg2)")
	}
	if !strings.Contains(err.Error(), "db/password") {
		t.Fatalf("Run(t) error = %v, want it to name the missing secret db/password", err)
	}
	if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
		t.Fatalf("%s must not have been written: a credentials file silently written with an "+
			"empty secret is exactly the bug task hg2 fixes", target)
	}

	// declerr.First() itself must not stay poisoned forever either: a clean
	// later record is what api.RecordPlanTo's own lastRecordFailure contract
	// relies on to unstick api.Apply (see AGENTS.md's "Registration-time
	// contract"); this call clears it so the next test starts fresh even if
	// t.Cleanup(ResetForTest) above were ever removed.
	_ = resource.ResetDeclarationError()
}

// TestArbitrarySinkLossMidRecordingDoesNotSwallowLaterFailure is the second,
// deliberately DIFFERENT route hg2's annotation asks for: instead of going
// through either resource.ResetForTest or resource.ResetDeclarationError, the
// task body calls internal/declerr.Reset directly — standing in for any
// future code path (inside gonf or a resource package) that has the same
// sink-clearing effect, by accident or design, without gonf ever having seen
// or reasoned about that specific call site. If the fix were narrowly
// patching known routes (the tf2 approach) this would still slip through;
// because the real fix is RecordPlanTo re-checking declerr.First()
// unconditionally after recordPlanBody succeeds, it catches this route too
// with no route-specific code at all.
func TestArbitrarySinkLossMidRecordingDoesNotSwallowLaterFailure(t *testing.T) {
	ResetForTest()
	useSecretWorkDir(t)
	t.Cleanup(ResetForTest)

	target := filepath.Join(t.TempDir(), "creds.txt")
	Task("t", "", func() {
		// Stand-in for an arbitrary, gonf-internal future call that clears
		// declerr's sink as a side effect — not resource.ResetForTest, not
		// resource.ResetDeclarationError, just the package primitive both of
		// them are ultimately built on.
		declerr.Reset()
		s := MustSecret("db/password")
		File(target, options.WithContent("password="+s))
	})

	err := Run("t")
	if err == nil {
		t.Fatal("Run(t) = nil, want the MustSecret failure to fail the record even though the sink " +
			"was lost through a route neither ResetForTest nor ResetDeclarationError name — the " +
			"post-record declerr.First() re-check (task hg2) must be route-agnostic")
	}
	if !strings.Contains(err.Error(), "db/password") {
		t.Fatalf("Run(t) error = %v, want it to name the missing secret db/password", err)
	}
	if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
		t.Fatalf("%s must not have been written: a credentials file silently written with an "+
			"empty secret is exactly the bug task hg2 fixes", target)
	}

	_ = resource.ResetDeclarationError()
}
