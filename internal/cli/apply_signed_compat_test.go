package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/snonux/gonf/internal"
	"github.com/snonux/gonf/plan/seal"
)

// Compatibility, flag and unit tests for task 8g2: without the new flags
// unsigned input behaves exactly as before; a signed plan without
// -trusted-signers still gets verified (against the default file) instead
// of being unwrapped unchecked; the flags refuse nonsense values; and the
// capability probe prints the envelope version.

// newSignedWords are words only the signing code prints; an apply of
// unsigned input without the new flags must print none of them.
var newSignedWords = []string{"signature", "signed", "trusted", "warning", "-require-signed"}

// TestCLIApplyPlainAndSealedWithoutNewFlagsUnchanged is security requirement 5:
// an unsigned plan.age (file and stdin) and a plan.jsonl apply exactly as
// before, with the same summaries and none of the new wording.
func TestCLIApplyPlainAndSealedWithoutNewFlagsUnchanged(t *testing.T) {
	fx := newSignedFixture(t)
	planPath := fx.writePlan(t, "plan.age", fx.sealed)
	code, stdout, stderr := runGonfCaptureStdout(t, "apply", "-identity", fx.identityPath, planPath)
	if code != 0 || stdout != "decrypted and applied "+planPath+" (2 ops)\n" {
		t.Fatalf("sealed file: exit %d stdout %q stderr %q", code, stdout, stderr)
	}
	requireAbsent(t, newSignedWords, stdout, stderr)
	fx.requireApplied(t)

	_ = os.Remove(fx.target)
	feedStdin(t, fx.sealed)
	code, stderr = runGonf(t, "apply", "-identity", fx.identityPath, "-")
	if code != 0 || !strings.HasSuffix(stderr, "decrypted and applied stdin (2 ops)\n") {
		t.Fatalf("sealed stdin: exit %d stderr %q", code, stderr)
	}
	requireAbsent(t, newSignedWords, stderr)

	jsonl := fx.writePlan(t, "plan.jsonl", []byte(`{"op":"plan","version":1,"id":"p"}`+"\n"))
	code, stdout, stderr = runGonfCaptureStdout(t, "apply", jsonl)
	if code != 0 || stdout != "applied "+jsonl+" (1 ops)\n" {
		t.Fatalf("plan.jsonl: exit %d stdout %q stderr %q", code, stdout, stderr)
	}
	requireAbsent(t, newSignedWords, stdout, stderr)
}

// TestCLIApplySignedWithoutFlagsUsesDefaultTrust pins how a signed plan
// behaves with no signing flag at all (docs/design/plan-signing.md "Verification
// order" step 2): it is still verified, against the non-root default
// ${XDG_CONFIG_HOME}/gonf/trusted-signers, and refused before decryption
// when that file is missing. It is never unwrapped unchecked.
func TestCLIApplySignedWithoutFlagsUsesDefaultTrust(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root has no default trusted-signers path (-trusted-signers is required)")
	}
	fx := newSignedFixture(t)
	config := filepath.Join(fx.dir, "config")
	t.Setenv("XDG_CONFIG_HOME", config)
	env := fx.envelope(t, signedTestNow)
	runSignedRefusal(t, fx, env, false, "trusted-signers file "+filepath.Join(config, "gonf", "trusted-signers"))

	if err := os.MkdirAll(filepath.Join(config, "gonf"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeModeFile(t, filepath.Join(config, "gonf", "trusted-signers"), fx.signer.Public().String()+"\n", 0o644)
	code, stderr := runGonf(t, "apply", "-identity", fx.identityPath, fx.writePlan(t, "signed.age", env))
	if code != 0 || !strings.Contains(stderr, "signature verified") {
		t.Fatalf("exit %d stderr %q, want the default trusted-signers file used", code, stderr)
	}
	fx.requireApplied(t)
}

// TestCLIApplyTrustedSignersWithoutRequireWarnsOnUnsigned: -trusted-signers
// alone does not refuse unsigned input (the design keeps an unsigned
// plan.age valid for interactive apply), but says so, so an operator who
// expected enforcement learns about -require-signed.
func TestCLIApplyTrustedSignersWithoutRequireWarnsOnUnsigned(t *testing.T) {
	fx := newSignedFixture(t)
	code, stderr := runGonf(t, "apply", "-identity", fx.identityPath, "-trusted-signers", fx.trustedPath,
		fx.writePlan(t, "plan.age", fx.sealed))
	want := "is not a signed plan; -trusted-signers only checks signed plans (add -require-signed to refuse unsigned input)"
	if code != 0 || !strings.Contains(stderr, "apply: warning: ") || !strings.Contains(stderr, want) {
		t.Fatalf("exit %d stderr %q, want the plan applied with the warning", code, stderr)
	}
	fx.requireApplied(t)
}

// TestCLIApplySigningFlagUsageErrors: nonsense signing flags and a signed
// stream combined with the push-only stdin flags are usage errors (exit 2)
// that decrypt and apply nothing.
func TestCLIApplySigningFlagUsageErrors(t *testing.T) {
	fx := newSignedFixture(t)
	env := fx.envelope(t, signedTestNow)
	planPath := fx.writePlan(t, "signed.age", env)
	cases := []struct {
		name, want string
		args       []string
		stdin      bool
	}{
		{"zero max age", "-max-signed-age must be positive", []string{"-max-signed-age", "0", planPath}, false},
		{"negative max age", "-max-signed-age must be positive", []string{"-max-signed-age", "-1h", planPath}, false},
		{"empty trusted path", "-trusted-signers needs a file path", []string{"-trusted-signers", "", planPath}, false},
		{"strict preview", "-strict-preview cannot be combined with a signed plan stream", []string{"-strict-preview", "-"}, true},
		{"apply dir", "-apply-dir cannot be combined with a signed plan stream", []string{"-apply-dir", fx.dir, "-"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.stdin {
				feedStdin(t, env)
			}
			args := append([]string{"apply", "-trusted-signers", fx.trustedPath, "-identity", fx.identityPath}, tc.args...)
			code, stderr := runGonf(t, args...)
			if code != 2 || !strings.Contains(stderr, tc.want) {
				t.Fatalf("exit %d stderr %q, want exit 2 with %q", code, stderr, tc.want)
			}
			fx.requireNothingApplied(t)
		})
	}
}

// TestCLISignedVersionFlag is security requirement 6: gonf -signed-version
// prints the envelope version this binary verifies, the N of the magic
// plan/seal checks.
func TestCLISignedVersionFlag(t *testing.T) {
	code, stdout, stderr := runGonfCaptureStdout(t, "-signed-version")
	if code != 0 || stdout != "1\n" || stderr != "" {
		t.Fatalf("exit %d stdout %q stderr %q, want \"1\\n\"", code, stdout, stderr)
	}
	if seal.SignedPlanMagic != "GONF-SIGNED-PLAN/"+strconv.Itoa(internal.SignedVersion) {
		t.Fatalf("SignedVersion %d does not match the verified magic %q", internal.SignedVersion, seal.SignedPlanMagic)
	}
}

func TestClassifyPlanInput(t *testing.T) {
	cases := map[string]planInputKind{
		"GONF-SIGNED-PLAN/1\nkey":       signedInput,
		"GONF-SIGNED-PLAN/9":            signedInput,
		"age-encryption.org/v1\n-> X":   sealedInput,
		"age-encryption.org/v1":         sealedInput,
		"GONF-PUSH/1\n":                 plainInput,
		`{"op":"plan"}`:                 plainInput,
		"":                              plainInput,
		"GONF-SIGNED-PLAN":              plainInput,
		"age-encryption.org/v1 trailer": plainInput,
	}
	for in, want := range cases {
		if got := classifyPlanInput([]byte(in)); got != want {
			t.Errorf("classifyPlanInput(%q) = %d, want %d", in, got, want)
		}
	}
	if planSniffLen < seal.SignedSniffLen || planSniffLen < len(ageMagicLine) {
		t.Fatalf("planSniffLen %d cannot hold both magics", planSniffLen)
	}
}

func TestCheckSignedFreshnessBoundaries(t *testing.T) {
	now := signedTestNow.Add(999 * time.Millisecond) // truncated to the second, like signed-at
	cases := []struct {
		offset time.Duration
		want   error
	}{
		{-time.Hour, nil},
		{-defaultMaxSignedAge, nil},
		{-defaultMaxSignedAge - time.Second, errSignedPlanStale},
		{signedClockSkew, nil},
		{signedClockSkew + time.Second, errSignedPlanFuture},
		{0, nil},
	}
	for _, tc := range cases {
		err := checkSignedFreshness(signedTestNow.Add(tc.offset), now, defaultMaxSignedAge)
		if !errors.Is(err, tc.want) || (tc.want == nil && err != nil) {
			t.Errorf("offset %v: err %v, want %v", tc.offset, err, tc.want)
		}
	}
}

func TestReadSignedStreamCap(t *testing.T) {
	if got, err := readSignedStream(bytes.NewReader(make([]byte, 10)), 10); err != nil || len(got) != 10 {
		t.Fatalf("at the limit: %d bytes, %v", len(got), err)
	}
	if _, err := readSignedStream(bytes.NewReader(make([]byte, 11)), 10); !errors.Is(err, errSignedEnvelopeTooLarge) {
		t.Fatalf("over the limit: %v, want errSignedEnvelopeTooLarge", err)
	}
	if maxSignedEnvelopeBytes <= maxSealedFrameBytes {
		t.Fatal("the envelope cap must leave room for age's overhead over the frame cap")
	}
}
