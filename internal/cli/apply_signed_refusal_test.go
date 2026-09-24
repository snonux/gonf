package cli

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/snonux/gonf/plan/seal"
)

// Refusal tests for task 8g2: every way a signed plan can fail is refused
// with exit 1 before anything is decrypted (the -identity path does not
// exist, so reaching decryption would print an identity error instead), the
// freshness window is checked only once the signature verified, and the
// trusted-signers file gets 6g2's hardening.

// replaceEnvelopeLine returns env with its header line i (0 magic, 1 key,
// 2 signature, 3 signed-at) replaced by line.
func replaceEnvelopeLine(t *testing.T, env []byte, i int, line string) []byte {
	t.Helper()
	parts := bytes.SplitN(env, []byte("\n"), 5)
	if len(parts) != 5 {
		t.Fatalf("envelope has %d parts", len(parts))
	}
	parts[i] = []byte(line)
	return bytes.Join(parts, []byte("\n"))
}

// flipFirstChar changes s's first character to another base64 character.
func flipFirstChar(s string) string {
	if s[0] == 'A' {
		return "B" + s[1:]
	}
	return "A" + s[1:]
}

// undatedEnvelope is v0.17.0's library layout (task 6g2 before 7g2): magic,
// key, signature over magic||plan.age, then plan.age, with no signed-at
// line, correctly signed by a key the returned trusted line names.
func undatedEnvelope(t *testing.T, sealed []byte) (env []byte, trustedLine string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	enc := base64.RawStdEncoding
	msg := append([]byte(seal.SignedPlanMagic+"\n"), sealed...)
	head := seal.SignedPlanMagic + "\n" + enc.EncodeToString(pub) + "\n" + enc.EncodeToString(ed25519.Sign(priv, msg)) + "\n"
	return append([]byte(head), sealed...), "gonf-signer-ed25519 " + enc.EncodeToString(pub)
}

// runSignedRefusal applies data (as a file, or on stdin) with a missing
// -identity and the given extra flags, and checks the refusal shape: exit
// 1, want in stderr, nothing decrypted, applied or leaked, no claim of
// verification.
func runSignedRefusal(t *testing.T, fx *signedFixture, data []byte, stdin bool, want string, flags ...string) {
	t.Helper()
	arg := "-"
	if stdin {
		feedStdin(t, data)
	} else {
		arg = fx.writePlan(t, "signed.age", data)
	}
	args := append([]string{"apply", "-identity", filepath.Join(fx.dir, "no-such-identity")}, flags...)
	code, stdout, stderr := runGonfCaptureStdout(t, append(args, arg)...)
	if code != 1 || !strings.Contains(stderr, want) || !strings.Contains(stderr, "nothing decrypted or applied") {
		t.Fatalf("exit %d stderr %q, want exit 1 with %q", code, stderr, want)
	}
	if strings.Contains(stderr, "identity") || strings.Contains(stderr, "verified") || stdout != "" {
		t.Fatalf("refusal must precede decryption and claim nothing: stdout %q stderr %q", stdout, stderr)
	}
	fx.requireNothingApplied(t)
	fx.requireRefusalNoLeak(t, stdout, stderr)
}

// TestCLIApplySignedRefusedBeforeDecrypt is security requirement 2: an
// unknown signer, a bad signature, a tampered payload or signed-at time and
// a malformed envelope (truncated, another version, v0.17.0's undated
// layout) are all refused before decryption, on a file and on stdin, and
// the refusal never echoes the envelope's key.
func TestCLIApplySignedRefusedBeforeDecrypt(t *testing.T) {
	fx := newSignedFixture(t)
	good := fx.envelope(t, signedTestNow)
	other, err := seal.GenerateSigner()
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := seal.SignAt(fx.sealed, other, signedTestNow)
	if err != nil {
		t.Fatal(err)
	}
	sigLine := string(bytes.SplitN(good, []byte("\n"), 4)[2])
	tampered := bytes.Clone(good)
	tampered[len(tampered)-1] ^= 1
	undated, undatedTrusted := undatedEnvelope(t, fx.sealed)
	undatedTrust := filepath.Join(fx.dir, "undated-trusted")
	writeModeFile(t, undatedTrust, undatedTrusted+"\n", 0o644)
	cases := []struct {
		name, want, trusted string
		env                 []byte
	}{
		{"unknown signer", "signed by a key that is not a trusted signer", fx.trustedPath, foreign},
		{"bad signature", "signature does not verify", fx.trustedPath, replaceEnvelopeLine(t, good, 2, flipFirstChar(sigLine))},
		{"tampered payload", "signature does not verify", fx.trustedPath, tampered},
		{"tampered signed-at", "signature does not verify", fx.trustedPath, replaceEnvelopeLine(t, good, 3, "signed-at 2026-09-24T12:00:01Z")},
		{"truncated header", "malformed signed-plan envelope", fx.trustedPath, good[:len(seal.SignedPlanMagic)+30]},
		{"other version", "malformed signed-plan envelope", fx.trustedPath, replaceEnvelopeLine(t, good, 0, "GONF-SIGNED-PLAN/2")},
		{"v0.17.0 undated layout", "malformed signed-plan envelope", undatedTrust, undated},
	}
	for _, tc := range cases {
		for _, stdin := range []bool{false, true} {
			t.Run(tc.name+map[bool]string{false: " file", true: " stdin"}[stdin], func(t *testing.T) {
				runSignedRefusal(t, fx, tc.env, stdin, "signed plan refused: plan/seal: "+tc.want,
					"-trusted-signers", tc.trusted, "-require-signed")
			})
		}
	}
	code, stderr := runGonf(t, "apply", "-trusted-signers", fx.trustedPath, fx.writePlan(t, "f.age", foreign))
	if code != 1 || strings.Contains(stderr, strings.Fields(other.Public().String())[1]) {
		t.Fatalf("unknown-signer refusal must not echo the envelope key: exit %d stderr %q", code, stderr)
	}
}

// TestCLIApplySignedFreshnessWindow is security requirement 3: the window
// boundaries (default 24h back, 5m clock skew forward), -max-signed-age,
// and that a stale or future signed-at is only judged after the signature
// verified (a bad signature on a stale envelope is refused as a bad
// signature, never as stale).
func TestCLIApplySignedFreshnessWindow(t *testing.T) {
	fx := newSignedFixture(t)
	accept := []struct {
		name  string
		at    time.Time
		flags []string
	}{
		{"exactly max age", signedTestNow.Add(-defaultMaxSignedAge), nil},
		{"exactly skew ahead", signedTestNow.Add(signedClockSkew), nil},
		{"stale by default, inside -max-signed-age 48h", signedTestNow.Add(-25 * time.Hour), []string{"-max-signed-age", "48h"}},
	}
	for _, tc := range accept {
		t.Run(tc.name, func(t *testing.T) {
			_ = os.Remove(fx.target)
			args := append([]string{"apply", "-identity", fx.identityPath, "-trusted-signers", fx.trustedPath}, tc.flags...)
			code, stderr := runGonf(t, append(args, fx.writePlan(t, "fresh.age", fx.envelope(t, tc.at)))...)
			if code != 0 {
				t.Fatalf("exit %d stderr %q, want the plan applied", code, stderr)
			}
			fx.requireApplied(t)
		})
	}
	_ = os.Remove(fx.target)
	stale := fx.envelope(t, signedTestNow.Add(-defaultMaxSignedAge-time.Second))
	future := fx.envelope(t, signedTestNow.Add(signedClockSkew+time.Second))
	runSignedRefusal(t, fx, stale, false, "signed plan is too old: signed at 2026-09-23T11:59:59Z, more than "+
		"-max-signed-age 24h0m0s before this host's clock (2026-09-24T12:00:00Z)", "-trusted-signers", fx.trustedPath)
	runSignedRefusal(t, fx, stale, true, "signed plan is too old", "-trusted-signers", fx.trustedPath)
	runSignedRefusal(t, fx, future, false, "signed plan is dated in the future: signed at 2026-09-24T12:05:01Z, "+
		"more than 5m0s after this host's clock", "-trusted-signers", fx.trustedPath)
	runSignedRefusal(t, fx, fx.envelope(t, signedTestNow.Add(-time.Hour)), false, "too old",
		"-trusted-signers", fx.trustedPath, "-max-signed-age", "30m")
	badSig := replaceEnvelopeLine(t, stale, 2, flipFirstChar(string(bytes.SplitN(stale, []byte("\n"), 4)[2])))
	runSignedRefusal(t, fx, badSig, false, "signature does not verify", "-trusted-signers", fx.trustedPath)
	other, err := seal.GenerateSigner()
	if err != nil {
		t.Fatal(err)
	}
	foreignFuture, err := seal.SignAt(fx.sealed, other, signedTestNow.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	runSignedRefusal(t, fx, foreignFuture, false, "not a trusted signer", "-trusted-signers", fx.trustedPath)
}

// TestCLIApplyTrustedSignersHardening is security requirement 4: the
// trusted-signers file goes through plan/seal.LoadTrustedSigners, so a
// symlink (at the file or above it), a group- or world-writable file, a
// missing or empty file, a byte-order mark and a weak key are all refused
// before decryption, as is root without an explicit -trusted-signers.
func TestCLIApplyTrustedSignersHardening(t *testing.T) {
	fx := newSignedFixture(t)
	env := fx.envelope(t, signedTestNow)
	line := fx.signer.Public().String() + "\n"
	path := func(name, content string, mode os.FileMode) string {
		p := filepath.Join(fx.dir, name)
		writeModeFile(t, p, content, mode)
		return p
	}
	link := filepath.Join(fx.dir, "trusted-link")
	if err := os.Symlink(fx.trustedPath, link); err != nil {
		t.Fatal(err)
	}
	linkedDir := filepath.Join(fx.dir, "linked-dir")
	if err := os.Symlink(fx.dir, linkedDir); err != nil {
		t.Fatal(err)
	}
	weak := "gonf-signer-ed25519 " + base64.RawStdEncoding.EncodeToString(make([]byte, 32)) + "\n"
	cases := map[string]string{
		"trusted-signers file path contains a symlink":       link,
		"trusted-signers file path contains a symlink (dir)": filepath.Join(linkedDir, "trusted-signers"),
		"trusted-signers file is writable by group or other": path("group-writable", line, 0o664),
		"writable by group or other (world)":                 path("world-writable", line, 0o646),
		"file does not exist":                                filepath.Join(fx.dir, "missing"),
		"no trusted signers":                                 path("empty", "# nothing\n", 0o644),
		"byte-order mark":                                    path("bom", "\xef\xbb\xbf"+line, 0o644),
		"small-order or non-canonical":                       path("weak", weak, 0o644),
	}
	for want, trusted := range cases {
		t.Run(want, func(t *testing.T) {
			want = strings.TrimSuffix(strings.TrimSuffix(want, " (dir)"), " (world)")
			if strings.HasPrefix(want, "writable") {
				want = "trusted-signers file is " + want
			}
			runSignedRefusal(t, fx, env, false, want, "-trusted-signers", trusted)
		})
	}
	origEuid := geteuid
	geteuid = func() int { return 0 }
	t.Cleanup(func() { geteuid = origEuid })
	runSignedRefusal(t, fx, env, false, "running as root (euid 0); -trusted-signers is required")
}
