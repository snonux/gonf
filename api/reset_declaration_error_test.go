package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/resource"
)

// These tests pin tasks tf2 and vf2, two closely related fixes to
// resource.ResetDeclarationError (task oe2's production-safe escape hatch
// for the sticky internal/declerr state — see its doc comment and AGENTS.md's
// "Registration-time contract"):
//
//   - tf2 (HIGH): ResetDeclarationError used to call internal/declerr.Reset,
//     which clears the capture sink an active RecordPlanTo recording installs
//     as well as the sticky first error. Calling it from inside a task body
//     WHILE that recording is active silently disabled the recording's
//     capture for the rest of the body, so a later declaration error (e.g. a
//     failed MustSecret) missed it and landed on the process-wide sticky slot
//     instead — nothing re-checks that slot once a record completes, so the
//     record finished as if nothing had failed, silently writing a
//     credentials file with an empty secret. Fixed with internal/declerr.
//     ResetFirst, which clears only the sticky first error.
//   - vf2 (MEDIUM): ResetDeclarationError could not tell a safe-to-clear
//     declaration error (a resource-ID collision) from an unsafe one (a
//     failed MustSecret/OptionalSecret/ResolveSecret lookup, whose resource
//     is already registered holding an empty value). Fixed by having it
//     return the error it discarded, so a caller must look at what it is
//     clearing instead of it happening silently.

// TestResetDeclarationErrorMidRecordingDoesNotSwallowLaterFailure reproduces
// tf2's exact probe: a task body that defensively calls
// resource.ResetDeclarationError() (its doc comment always scoped the
// supported use to the direct-apply path, never mid-recording, but nothing
// enforced that) and then goes on to declare a resource from a failing
// MustSecret call. Before tf2's fix this passed silently: Run returned nil,
// and the target file was written to disk containing "password=" with the
// secret empty. After the fix the later MustSecret failure must still reach
// the active recording's capture sink and fail the record loudly, exactly as
// it would without the ResetDeclarationError() call in between.
func TestResetDeclarationErrorMidRecordingDoesNotSwallowLaterFailure(t *testing.T) {
	ResetForTest()
	useSecretWorkDir(t) // no "secrets" dir here: MustSecret("db/password") fails
	t.Cleanup(ResetForTest)

	target := filepath.Join(t.TempDir(), "creds.txt")
	Task("t", "", func() {
		// The defensive (and, per ResetDeclarationError's doc comment,
		// unsupported) mid-recording call tf2's annotation reproduces.
		_ = resource.ResetDeclarationError()
		s := MustSecret("db/password")
		File(target, options.WithContent("password="+s))
	})

	err := Run("t")
	if err == nil {
		t.Fatal("Run(t) = nil, want the MustSecret failure to fail the record: " +
			"ResetDeclarationError must not have swallowed a later report in the same recording (task tf2)")
	}
	if !strings.Contains(err.Error(), "db/password") {
		t.Fatalf("Run(t) error = %v, want it to name the missing secret db/password", err)
	}
	if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
		t.Fatalf("%s must not have been written: a credentials file silently written with an "+
			"empty secret is exactly the bug task tf2 fixes", target)
	}
}

// TestResetDeclarationErrorReturnsDiscardedSecretFailure reproduces vf2's
// exact probe on the DIRECT apply path (distinct from tf2's mid-recording
// scenario above): File(good, "good"), a failing MustSecret, then
// File(secretTxt, "password="+s). The first Apply() correctly refuses. This
// pins two things: (1) ResetDeclarationError() now returns the discarded
// error, non-nil, naming the secret lookup that failed, so a caller cannot
// clear it without at least seeing what it discarded; (2) a caller who reads
// that error but ignores it and calls Apply() again WITHOUT also calling
// resource.ResetRepository() still gets the same outcome as before vf2: both
// files are written, and secretTxt contains the literal "password=" with the
// secret empty. vf2 does not — and its own annotation says should not — make
// this outcome impossible; it makes it visible instead, by forcing the
// caller to receive (even if it then discards) the very error that names the
// unsafe class. The safe remediation (also calling resource.ResetRepository
// so every resource re-declares from scratch) is documented on
// ResetDeclarationError's and api.Apply's doc comments and AGENTS.md's
// "Registration-time contract", not enforced by the type system.
func TestResetDeclarationErrorReturnsDiscardedSecretFailure(t *testing.T) {
	ResetForTest()
	useSecretWorkDir(t)
	t.Cleanup(ResetForTest)

	good := filepath.Join(t.TempDir(), "good")
	secretTxt := filepath.Join(t.TempDir(), "secret.txt")

	File(good, options.WithContent("good"))
	s := MustSecret("db/password") // fails: no secrets directory
	File(secretTxt, options.WithContent("password="+s))

	if err := Apply(); err == nil {
		t.Fatal("first Apply() = nil, want the MustSecret failure to refuse it")
	}

	discarded := resource.ResetDeclarationError()
	if discarded == nil {
		t.Fatal("ResetDeclarationError() = nil, want the discarded MustSecret failure " +
			"(task vf2: the caller must be able to see what it is discarding)")
	}
	if !strings.Contains(discarded.Error(), "db/password") {
		t.Fatalf("ResetDeclarationError() = %v, want it to name the failed secret lookup db/password", discarded)
	}

	// The caller looked at the returned error (this test just did, above)
	// but — like the pre-vf2 code path always did — ignores it and calls
	// Apply() again without also resetting the repository. This is the
	// caller-responsibility outcome vf2's annotation says the fix should
	// still allow, deliberately: it must be visible, not impossible.
	if err := Apply(); err != nil {
		t.Fatalf("second Apply() after ResetDeclarationError = %v, want nil: clearing the "+
			"sticky error alone is enough for Apply to proceed", err)
	}
	if got, err := os.ReadFile(secretTxt); err != nil || string(got) != "password=" {
		t.Fatalf("%s = (%q, %v), want (\"password=\", nil): the resource registered with an "+
			"empty secret before the failure was still applied because the caller never called "+
			"resource.ResetRepository() — exactly the outcome ResetDeclarationError's doc comment warns about",
			secretTxt, got, err)
	}
	if got, err := os.ReadFile(good); err != nil || string(got) != "good" {
		t.Fatalf("%s = (%q, %v), want (\"good\", nil)", good, got, err)
	}
}

// TestResetDeclarationErrorSafeRemediationResetRepositoryAvoidsEmptySecret
// pins the documented safe remediation for the unsafe (secret-resolution)
// class vf2's doc comment names: after clearing the discarded error, ALSO
// call resource.ResetRepository() and re-declare, rather than just clearing
// and continuing with the half-registered set. With the underlying failure
// fixed (the secret now resolves), re-declaring writes the real secret
// instead of an empty one.
func TestResetDeclarationErrorSafeRemediationResetRepositoryAvoidsEmptySecret(t *testing.T) {
	ResetForTest()
	useSecretWorkDir(t)
	t.Cleanup(ResetForTest)

	good := filepath.Join(t.TempDir(), "good")
	secretTxt := filepath.Join(t.TempDir(), "secret.txt")

	declare := func() {
		File(good, options.WithContent("good"))
		s := MustSecret("db/password")
		File(secretTxt, options.WithContent("password="+s))
	}
	declare()
	if err := Apply(); err == nil {
		t.Fatal("first Apply() = nil, want it refused by the missing secret")
	}
	if discarded := resource.ResetDeclarationError(); discarded == nil {
		t.Fatal("ResetDeclarationError() = nil, want the discarded secret-lookup failure")
	}

	// Safe remediation: wipe the registered repository too, then re-declare
	// once the recipe can actually resolve the secret.
	resource.ResetRepository()
	writeSecret(t, "db/password", "hunter2")
	declare()

	if err := Apply(); err != nil {
		t.Fatalf("Apply() after the safe remediation = %v, want nil", err)
	}
	if got, err := os.ReadFile(secretTxt); err != nil || string(got) != "password=hunter2" {
		t.Fatalf("%s = (%q, %v), want (\"password=hunter2\", nil): re-declaring from scratch after "+
			"ResetRepository must pick up the real secret, not an empty one", secretTxt, got, err)
	}
	if got, err := os.ReadFile(good); err != nil || string(got) != "good" {
		t.Fatalf("%s = (%q, %v), want (\"good\", nil)", good, got, err)
	}
}
