package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/snonux/gonf/internal/testseam"
	"github.com/snonux/gonf/plan/seal"
)

// Tests for task 8g2 (signing phase 3): `gonf apply -trusted-signers
// [-require-signed] [-max-signed-age]` on files and stdin, driven through
// the real CLI entry point. docs/design/plan-signing.md "Verification order" is
// the contract: a signed plan is verified BEFORE any decryption, its
// freshness is checked only once the signature verified, -require-signed
// refuses unsigned input outright, and without the new flags unsigned
// input behaves exactly as before.

// signedTestNow is the destination clock every signed-apply test pins.
var signedTestNow = time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)

// setSignedPlanClock pins signedPlanNow to now for one test. The clock is
// package-global, so the test must not run in parallel (the guard env
// makes testing panic if it does).
func setSignedPlanClock(t *testing.T, now time.Time) {
	t.Helper()
	t.Setenv(testseam.ParallelGuardEnv, "1")
	old := signedPlanNow
	signedPlanNow = func() time.Time { return now }
	t.Cleanup(func() { signedPlanNow = old })
}

// signedFixture is one destination: an identity, a trusted-signers file
// holding the fixture signer, a sealed plan writing "hi\n" to target, and
// the secrets that must never appear in any output.
type signedFixture struct {
	dir, stagingRoot, identityPath, trustedPath, target string
	identitySecret, signerSecret, signerKey             string
	signer                                              seal.Signer
	sealed                                              []byte
}

// newSignedFixture builds a signedFixture with a plain (blob-free) sealed
// plan and pins the destination clock to signedTestNow.
func newSignedFixture(t *testing.T) *signedFixture {
	t.Helper()
	setSignedPlanClock(t, signedTestNow)
	fx := &signedFixture{dir: t.TempDir(), stagingRoot: isolateSealedStagingRoot(t)}
	identityLine, recipientLine := sealedKeyPair(t)
	fx.identitySecret = identityLine
	fx.identityPath = writeSealedIdentityFile(t, fx.dir, "identity", identityLine)
	fx.target = filepath.Join(fx.dir, "out.txt")
	fx.sealed = sealFrame(t, touchFramePush(t, "signed", fx.target), []string{recipientLine})
	signer, err := seal.GenerateSigner()
	if err != nil {
		t.Fatal(err)
	}
	signerPath := filepath.Join(fx.dir, "signer")
	if err := seal.WriteSignerFile(signerPath, signer); err != nil {
		t.Fatal(err)
	}
	fx.signer, fx.signerSecret = signer, signerSecret(t, signerPath)
	fx.signerKey = strings.Fields(signer.Public().String())[1]
	fx.trustedPath = filepath.Join(fx.dir, "trusted-signers")
	writeModeFile(t, fx.trustedPath, signer.Public().String()+"\n", 0o644)
	return fx
}

// envelope signs fx.sealed with fx.signer at at.
func (fx *signedFixture) envelope(t *testing.T, at time.Time) []byte {
	t.Helper()
	env, err := seal.SignAt(fx.sealed, fx.signer, at)
	if err != nil {
		t.Fatal(err)
	}
	return env
}

// writePlan writes data as dir/name (0600) and returns its path.
func (fx *signedFixture) writePlan(t *testing.T, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(fx.dir, name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// requireApplied fails unless the plan ran (target holds "hi\n").
func (fx *signedFixture) requireApplied(t *testing.T) {
	t.Helper()
	got, err := os.ReadFile(fx.target)
	if err != nil || string(got) != "hi\n" {
		t.Fatalf("target content = %q, %v; want the plan applied", got, err)
	}
}

// requireNothingApplied fails when the plan ran or a sealed run dir was
// left behind.
func (fx *signedFixture) requireNothingApplied(t *testing.T) {
	t.Helper()
	if _, err := os.Stat(fx.target); !os.IsNotExist(err) {
		t.Fatalf("refused apply must apply nothing, target exists: %v", err)
	}
	requireNoLeftoverSealedRunDirs(t, fx.stagingRoot)
}

// requireNoLeak fails when any output holds key material: the identity
// secret or the signer secret.
func (fx *signedFixture) requireNoLeak(t *testing.T, outputs ...string) {
	t.Helper()
	requireAbsent(t, []string{fx.identitySecret, fx.signerSecret, "AGE-SECRET-KEY"}, outputs...)
}

// requireRefusalNoLeak is requireNoLeak for a refused apply, which must
// also print no plaintext from inside the ciphertext: the target path,
// which only the encrypted plan names (a successful apply reports it).
func (fx *signedFixture) requireRefusalNoLeak(t *testing.T, outputs ...string) {
	t.Helper()
	fx.requireNoLeak(t, outputs...)
	requireAbsent(t, []string{fx.target}, outputs...)
}

// requireAbsent fails when any output contains any of needles.
func requireAbsent(t *testing.T, needles []string, outputs ...string) {
	t.Helper()
	for _, out := range outputs {
		for _, n := range needles {
			if strings.Contains(out, n) {
				t.Fatalf("output leaks %q: %q", n, out)
			}
		}
	}
}

func TestCLIApplySignedFileRoundTrip(t *testing.T) {
	fx := newSignedFixture(t)
	planPath := fx.writePlan(t, "plan.age", fx.envelope(t, signedTestNow.Add(-time.Hour)))

	code, stdout, stderr := runGonfCaptureStdout(t, "apply", "-identity", fx.identityPath,
		"-trusted-signers", fx.trustedPath, "-require-signed", planPath)
	if code != 0 {
		t.Fatalf("apply exit %d, stderr %q", code, stderr)
	}
	fx.requireApplied(t)
	if stdout != "decrypted and applied "+planPath+" (2 ops)\n" {
		t.Fatalf("stdout %q, want the unchanged sealed summary", stdout)
	}
	want := "apply: " + planPath + ": signature verified: trusted signer gonf-signer-ed25519 " + fx.signerKey
	if !strings.Contains(stderr, want) || !strings.Contains(stderr, "signed 2026-09-24T11:00:00Z (within -max-signed-age 24h0m0s)") {
		t.Fatalf("stderr %q missing the verified note %q", stderr, want)
	}
	fx.requireNoLeak(t, stdout, stderr)
}

func TestCLIApplySignedStdinWithBlobsCleansRunDir(t *testing.T) {
	fx := newSignedFixture(t)
	identityLine, recipientLine := sealedKeyPair(t)
	fx.identitySecret = identityLine
	fx.identityPath = writeSealedIdentityFile(t, fx.dir, "identity-blobs", identityLine)
	fx.sealed = sealFrame(t, blobFramePush(t, "signed-blobs", fx.target), []string{recipientLine})
	feedStdin(t, fx.envelope(t, signedTestNow))

	code, stderr := runGonf(t, "apply", "-identity", fx.identityPath, "-trusted-signers", fx.trustedPath, "-")
	if code != 0 {
		t.Fatalf("apply - exit %d, stderr %q", code, stderr)
	}
	got, err := os.ReadFile(fx.target)
	if err != nil || string(got) != "blob content\n" {
		t.Fatalf("target content = %q, %v", got, err)
	}
	if !strings.Contains(stderr, "apply: stdin: signature verified") ||
		!strings.Contains(stderr, "decrypted and applied stdin+blobs (2 ops)") {
		t.Fatalf("stderr %q missing the verified note or summary", stderr)
	}
	requireNoLeftoverSealedRunDirs(t, fx.stagingRoot)
	fx.requireNoLeak(t, stderr)
}

// TestCLIApplyRequireSignedRefusesUnsigned is security requirement 1: with
// -require-signed an unsigned plan.age, a plan.jsonl, a push frame and bare
// JSONL are refused (exit 1) before anything is decrypted or applied. The
// -identity path does not exist, so any decryption attempt would fail with
// an identity error instead of the -require-signed refusal.
func TestCLIApplyRequireSignedRefusesUnsigned(t *testing.T) {
	fx := newSignedFixture(t)
	missingIdentity := filepath.Join(fx.dir, "no-such-identity")
	jsonl := []byte(`{"op":"plan","version":1,"id":"p"}` + "\n")
	cases := []struct {
		name  string
		data  []byte
		stdin bool
		want  string
	}{
		{"sealed file", fx.sealed, false, "is an unsigned sealed plan.age, not a GONF-SIGNED-PLAN/1 envelope; nothing decrypted or applied"},
		{"sealed stdin", fx.sealed, true, "stdin is an unsigned sealed plan.age"},
		{"jsonl file", jsonl, false, "is a plaintext plan (plan.jsonl, push frame or bare JSONL), not a GONF-SIGNED-PLAN/1 envelope; nothing applied"},
		{"push frame stdin", touchFramePush(t, "p", fx.target), true, "stdin is a plaintext plan"},
		{"empty stdin", nil, true, "stdin is a plaintext plan"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			arg := "-"
			if tc.stdin {
				feedStdin(t, tc.data)
			} else {
				arg = fx.writePlan(t, "unsigned.plan", tc.data)
			}
			code, stdout, stderr := runGonfCaptureStdout(t, "apply", "-identity", missingIdentity,
				"-trusted-signers", fx.trustedPath, "-require-signed", arg)
			if code != 1 || !strings.Contains(stderr, "apply: -require-signed: ") || !strings.Contains(stderr, tc.want) {
				t.Fatalf("exit %d stderr %q, want exit 1 and %q", code, stderr, tc.want)
			}
			if strings.Contains(stderr, "identity") || strings.Contains(strings.ToLower(stderr), "verified") {
				t.Fatalf("refusal must come before decryption and claim nothing verified: %q", stderr)
			}
			fx.requireNothingApplied(t)
			fx.requireRefusalNoLeak(t, stdout, stderr)
		})
	}
}

// TestCLIPlanSealSignThenApplyRequireSigned proves the two CLI halves
// interoperate: `gonf plan -seal -sign` (task 7g2) produces what `gonf
// apply -trusted-signers -require-signed` (task 8g2) verifies and applies,
// on the real clock.
func TestCLIPlanSealSignThenApplyRequireSigned(t *testing.T) {
	isolateXDGConfig(t)
	isolateSealedStagingRoot(t)
	work := registerSealTask(t)
	keyDir := t.TempDir()
	k := runKeygen(t, keyDir)
	recipient, identityLine := genSealKeyPair(t)
	identityPath := filepath.Join(keyDir, "identity")
	writeIdentityFile(t, identityPath, identityLine)
	out := filepath.Join(t.TempDir(), "out")
	if code, stderr := runGonf(t, "plan", "-o", out, "-seal", "-recipient", recipient, "-sign", k.path, "cli_seal_task"); code != 0 {
		t.Fatalf("plan -seal -sign exit %d, stderr %q", code, stderr)
	}
	code, stdout, stderr := runGonfCaptureStdout(t, "apply", "-identity", identityPath,
		"-trusted-signers", filepath.Join(keyDir, "trusted-signers"), "-require-signed", filepath.Join(out, "plan.age"))
	if code != 0 || !strings.Contains(stderr, "signature verified: trusted signer "+k.publicLine) {
		t.Fatalf("apply exit %d stdout %q stderr %q", code, stdout, stderr)
	}
	if got, err := os.ReadFile(filepath.Join(work, "plain")); err != nil || string(got) != "plain\n" {
		t.Fatalf("applied content %q, %v", got, err)
	}
	requireAbsent(t, []string{k.secret, identityLine}, stdout, stderr)
}
