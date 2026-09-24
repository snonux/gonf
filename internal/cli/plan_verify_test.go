package cli

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Tests for `gonf plan-verify` (task 8g2), the verify-only unwrap helper:
// on success stdout carries exactly the plan.age bytes that were signed,
// which then work on the emergency path (`... | gonf apply -`); on any
// refusal stdout stays empty.

func TestCLIPlanVerifyUnwrapsForEmergencyPath(t *testing.T) {
	fx := newSignedFixture(t)
	planPath := fx.writePlan(t, "signed.age", fx.envelope(t, signedTestNow.Add(-time.Minute)))
	code, stdout, stderr := runGonfCaptureStdout(t, "plan-verify", "-trusted-signers", fx.trustedPath, planPath)
	if code != 0 || !bytes.Equal([]byte(stdout), fx.sealed) {
		t.Fatalf("exit %d stderr %q; stdout must be the exact signed plan.age (%d bytes, got %d)",
			code, stderr, len(fx.sealed), len(stdout))
	}
	if !strings.Contains(stderr, "plan-verify: "+planPath+": signature verified: trusted signer") ||
		!strings.Contains(stderr, "still sealed, nothing decrypted") {
		t.Fatalf("stderr %q missing the verified report", stderr)
	}
	fx.requireNoLeak(t, stdout, stderr)

	// The unwrapped bytes are a bare plan.age again: the unsigned sealed
	// path (and so age -d | gonf apply -) takes them unchanged.
	feedStdin(t, []byte(stdout))
	code, stderr = runGonf(t, "apply", "-identity", fx.identityPath, "-")
	if code != 0 {
		t.Fatalf("apply of the unwrapped plan.age: exit %d stderr %q", code, stderr)
	}
	fx.requireApplied(t)
}

func TestCLIPlanVerifyStdin(t *testing.T) {
	fx := newSignedFixture(t)
	feedStdin(t, fx.envelope(t, signedTestNow))
	code, stdout, stderr := runGonfCaptureStdout(t, "plan-verify", "-trusted-signers", fx.trustedPath, "-")
	if code != 0 || !bytes.Equal([]byte(stdout), fx.sealed) || !strings.Contains(stderr, "plan-verify: stdin: signature verified") {
		t.Fatalf("exit %d stderr %q, stdout %d bytes", code, stderr, len(stdout))
	}
}

// TestCLIPlanVerifyRefusalsWriteNothing: an unsigned plan.age, a stale or
// tampered plan, a bad trusted-signers file and root without
// -trusted-signers are refused with exit 1 and an empty stdout.
func TestCLIPlanVerifyRefusalsWriteNothing(t *testing.T) {
	fx := newSignedFixture(t)
	groupWritable := filepath.Join(fx.dir, "group-writable")
	writeModeFile(t, groupWritable, fx.signer.Public().String()+"\n", 0o664)
	cases := []struct {
		name, want, trusted string
		data                []byte
	}{
		{"unsigned", "is not a signed plan (no GONF-SIGNED-PLAN/1 envelope); nothing written", fx.trustedPath, fx.sealed},
		{"stale", "signed plan refused: signed plan is too old", fx.trustedPath, fx.envelope(t, signedTestNow.Add(-25*time.Hour))},
		{"tampered", "signed plan refused: plan/seal: signature does not verify", fx.trustedPath,
			append(fx.envelope(t, signedTestNow), 'x')},
		{"writable trust file", "trusted-signers file is writable by group or other", groupWritable, fx.envelope(t, signedTestNow)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, stdout, stderr := runGonfCaptureStdout(t, "plan-verify", "-trusted-signers", tc.trusted,
				fx.writePlan(t, "in.age", tc.data))
			if code != 1 || stdout != "" || !strings.Contains(stderr, tc.want) || strings.Contains(stderr, "verified") {
				t.Fatalf("exit %d stdout %d bytes stderr %q, want exit 1, no output, %q", code, len(stdout), stderr, tc.want)
			}
			fx.requireRefusalNoLeak(t, stderr)
		})
	}
	origEuid := geteuid
	geteuid = func() int { return 0 }
	t.Cleanup(func() { geteuid = origEuid })
	code, stdout, stderr := runGonfCaptureStdout(t, "plan-verify", fx.writePlan(t, "root.age", fx.envelope(t, signedTestNow)))
	if code != 1 || stdout != "" || !strings.Contains(stderr, "-trusted-signers is required") {
		t.Fatalf("root: exit %d stdout %d bytes stderr %q", code, len(stdout), stderr)
	}
}

func TestCLIPlanVerifyUsage(t *testing.T) {
	for _, args := range [][]string{{"plan-verify"}, {"plan-verify", "a", "b"}, {"plan-verify", "-max-signed-age", "0", "a"}} {
		code, stderr := runGonf(t, args...)
		if code != 2 {
			t.Fatalf("%v: exit %d stderr %q, want 2", args, code, stderr)
		}
	}
}
