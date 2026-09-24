package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/plan/seal"
)

// Tests for task 7g2 (signing phase 2): `gonf plan-signer-keygen` and
// `gonf plan -seal -sign`, driven through the real CLI entry point and
// checked with the library's own seal.Verify and seal.Open, the same path
// a destination will take (task 8g2).

// signerKeys is one keygen run's result: the signer file, its public
// trusted-signers line, the secret seed text read back from the file (to
// prove it never appears in output) and the trusted set parsed from the
// public line.
type signerKeys struct {
	path, publicLine, secret string
	trusted                  []seal.TrustedSigner
}

// runKeygen runs `gonf plan-signer-keygen dir/signer` and loads what it
// wrote and printed.
func runKeygen(t *testing.T, dir string) signerKeys {
	t.Helper()
	path := filepath.Join(dir, "signer")
	code, stdout, stderr := runGonfCaptureStdout(t, "plan-signer-keygen", path)
	if code != 0 {
		t.Fatalf("keygen exit %d, stderr %q", code, stderr)
	}
	k := signerKeys{path: path, publicLine: strings.TrimSuffix(stdout, "\n"), secret: signerSecret(t, path)}
	if strings.Count(stdout, "\n") != 1 || !strings.HasPrefix(stdout, "gonf-signer-ed25519 ") {
		t.Fatalf("keygen stdout %q, want exactly the trusted-signers line", stdout)
	}
	requireNoSecret(t, k.secret, stdout, stderr)
	trustedPath := filepath.Join(dir, "trusted-signers")
	if err := os.WriteFile(trustedPath, []byte(stdout), 0o644); err != nil {
		t.Fatal(err)
	}
	trusted, err := seal.LoadTrustedSigners(trustedPath)
	if err != nil {
		t.Fatalf("LoadTrustedSigners of keygen's printed line: %v", err)
	}
	k.trusted = trusted
	return k
}

// signerSecret returns the base64 seed of path's GONF-SIGNER-SECRET-ED25519
// line.
func signerSecret(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if f := strings.Fields(line); len(f) == 2 && f[0] == "GONF-SIGNER-SECRET-ED25519" {
			return f[1]
		}
	}
	t.Fatalf("no secret line in %s", path)
	return ""
}

// requireNoSecret fails when any output holds the secret seed, or claims
// verification (docs/plan-signing.md: signing output never says
// "verified").
func requireNoSecret(t *testing.T, secret string, outputs ...string) {
	t.Helper()
	for _, out := range outputs {
		if strings.Contains(out, secret) {
			t.Fatalf("output leaks the signer secret: %q", out)
		}
		if strings.Contains(strings.ToLower(out), "verified") {
			t.Fatalf("output claims verification: %q", out)
		}
	}
}

// verifyAndOpen is the destination side: Verify env against trusted, then
// Open the unwrapped plan.age with identityLine and decode its frame.
func verifyAndOpen(t *testing.T, env []byte, trusted []seal.TrustedSigner, identityLine string) (seal.Verified, []plan.Op) {
	t.Helper()
	v, err := seal.Verify(env, trusted)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	idPath := filepath.Join(t.TempDir(), "identity")
	writeIdentityFile(t, idPath, identityLine)
	ids, err := seal.LoadIdentities(idPath)
	if err != nil {
		t.Fatal(err)
	}
	r, err := seal.Open(bytes.NewReader(v.Sealed), ids)
	if err != nil {
		t.Fatalf("Open of the verified payload: %v", err)
	}
	payload, err := plan.DecodePush(r, "")
	if err != nil {
		t.Fatalf("DecodePush: %v", err)
	}
	return v, payload.Ops
}

// requireSignedAtWithin fails unless v was signed between before and after.
func requireSignedAtWithin(t *testing.T, v seal.Verified, before, after time.Time) {
	t.Helper()
	if v.SignedAt.Before(before.Truncate(time.Second)) || v.SignedAt.After(after) {
		t.Fatalf("SignedAt %v not within [%v, %v]", v.SignedAt, before, after)
	}
}

func TestCLIPlanSignerKeygen(t *testing.T) {
	dir := t.TempDir()
	k := runKeygen(t, dir)
	info, err := os.Lstat(k.path)
	if err != nil || info.Mode() != 0o600 {
		t.Fatalf("signer file mode %v, %v; want a regular 0600 file", info.Mode(), err)
	}
	if _, err := seal.LoadSigner(k.path); err != nil {
		t.Fatalf("LoadSigner of the keygen file: %v", err)
	}
	before, _ := os.ReadFile(k.path)
	code, stdout, stderr := runGonfCaptureStdout(t, "plan-signer-keygen", k.path)
	if code != 1 || stdout != "" || !strings.Contains(stderr, "already exists") {
		t.Fatalf("second keygen: exit %d, stdout %q, stderr %q; want exit 1 refusing to overwrite", code, stdout, stderr)
	}
	if after, _ := os.ReadFile(k.path); !bytes.Equal(before, after) {
		t.Fatal("second keygen changed the existing signer file")
	}
	requireNoSecret(t, k.secret, stdout, stderr)
	if code, _ := runGonf(t, "plan-signer-keygen"); code != 2 {
		t.Fatalf("keygen with no path: exit %d, want 2", code)
	}
}

// TestCLIPlanSealSignRoundTrip is the whole operator flow: keygen, then
// plan -seal -sign, then the destination's Verify and Open.
func TestCLIPlanSealSignRoundTrip(t *testing.T) {
	isolateXDGConfig(t)
	registerSealTask(t)
	k := runKeygen(t, t.TempDir())
	recipient, identityLine := genSealKeyPair(t)
	dir := filepath.Join(t.TempDir(), "out")
	before := time.Now()
	code, stdout, stderr := runGonfCaptureStdout(t, "plan", "-o", dir, "-seal", "-recipient", recipient, "-sign", k.path, "cli_seal_task")
	after := time.Now()
	if code != 0 {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	env, err := os.ReadFile(filepath.Join(dir, "plan.age"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(env, []byte(seal.SignedPlanMagic+"\n")) {
		t.Fatal("plan.age is not a signed envelope")
	}
	v, ops := verifyAndOpen(t, env, k.trusted, identityLine)
	requireSignedAtWithin(t, v, before, after)
	if len(ops) == 0 {
		t.Fatal("decoded no ops")
	}
	want := "wrote " + filepath.Join(dir, "plan.age") + " (" + strconv.Itoa(len(ops)) + " ops, 1 recipients, signed " + v.SignedAt.Format(time.RFC3339) + ")\n"
	if !strings.HasPrefix(stdout, want) || !strings.Contains(stdout, "\n  signer "+k.publicLine+" sha256:") {
		t.Fatalf("stdout %q, want %q and a signer line naming %q", stdout, want, k.publicLine)
	}
	requireNoSecret(t, k.secret, stdout, stderr)
}

// TestCLIPlanSealSignStdout: -seal -stdout -sign prints the signed envelope
// itself and reports it on stderr.
func TestCLIPlanSealSignStdout(t *testing.T) {
	isolateXDGConfig(t)
	registerSealTask(t)
	k := runKeygen(t, t.TempDir())
	recipient, identityLine := genSealKeyPair(t)
	code, stdout, stderr := runGonfCaptureStdout(t, "plan", "-seal", "-stdout", "-recipient", recipient, "-sign", k.path, "cli_seal_task")
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	v, ops := verifyAndOpen(t, []byte(stdout), k.trusted, identityLine)
	want := "wrote stdout (" + strconv.Itoa(len(ops)) + " ops, 1 recipients, sealed, signed " + v.SignedAt.Format(time.RFC3339) + ")\n"
	if !strings.HasPrefix(stderr, want) || !strings.Contains(stderr, "  signer "+k.publicLine) {
		t.Fatalf("stderr %q, want %q and the signer line", stderr, want)
	}
	requireNoSecret(t, k.secret, stderr)
}

// TestCLIPlanSealSignForSignsEachHost: with -for every host's artifact is
// signed on its own, over its own ciphertext, and opens for its own host.
func TestCLIPlanSealSignForSignsEachHost(t *testing.T) {
	isolateXDGConfig(t)
	_, _, identity := registerSealForInventory(t)
	k := runKeygen(t, t.TempDir())
	operatorRecipient, _ := genSealKeyPair(t)
	dir := filepath.Join(t.TempDir(), "out")
	code, stdout, stderr := runGonfCaptureStdout(t, "plan", "-o", dir, "-seal", "-recipient", operatorRecipient,
		"-for", "edge", "-sign", k.path, "cli_seal_for_task")
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	var payloads [][]byte
	for _, host := range []string{"hostA", "hostB"} {
		env, err := os.ReadFile(filepath.Join(dir, "plan-"+host+".age"))
		if err != nil {
			t.Fatal(err)
		}
		v, ops := verifyAndOpen(t, env, k.trusted, identity[host])
		if !containsSubstring(fileOpContents(t, ops), "secret-for-"+host) {
			t.Fatalf("plan-%s.age does not carry %s's own plan", host, host)
		}
		payloads = append(payloads, v.Sealed)
	}
	if bytes.Equal(payloads[0], payloads[1]) {
		t.Fatal("both hosts got the same sealed payload")
	}
	if n := strings.Count(stdout, "  signer "+k.publicLine); n != 2 {
		t.Fatalf("stdout names the signer %d times, want once per host:\n%s", n, stdout)
	}
	requireNoSecret(t, k.secret, stdout, stderr)
}

// TestCLIPlanSealSignForStdout: -for one host with -stdout signs that
// host's artifact.
func TestCLIPlanSealSignForStdout(t *testing.T) {
	isolateXDGConfig(t)
	_, _, identity := registerSealForInventory(t)
	k := runKeygen(t, t.TempDir())
	operatorRecipient, _ := genSealKeyPair(t)
	code, stdout, stderr := runGonfCaptureStdout(t, "plan", "-seal", "-stdout", "-recipient", operatorRecipient,
		"-for", "hostA", "-sign", k.path, "cli_seal_for_task")
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	verifyAndOpen(t, []byte(stdout), k.trusted, identity["hostA"])
	if !strings.Contains(stderr, "sealed for host hostA, signed ") || !strings.Contains(stderr, "  signer "+k.publicLine) {
		t.Fatalf("stderr %q, want a signed report for hostA", stderr)
	}
}

// TestCLIPlanSealWithoutSignStaysUnsigned: no -sign, no envelope: the
// plain -seal output is a bare plan.age as before 7g2.
func TestCLIPlanSealWithoutSignStaysUnsigned(t *testing.T) {
	isolateXDGConfig(t)
	registerSealTask(t)
	recipient, _ := genSealKeyPair(t)
	code, stdout, stderr := runGonfCaptureStdout(t, "plan", "-seal", "-stdout", "-recipient", recipient, "cli_seal_task")
	if code != 0 || !strings.HasPrefix(stdout, "age-encryption.org/v1\n") {
		t.Fatalf("exit %d, stdout starts %q; want a bare age stream", code, stdout[:min(len(stdout), 24)])
	}
	if !strings.HasSuffix(strings.SplitN(stderr, "\n", 2)[0], " ops, 1 recipients, sealed)") || strings.Contains(stderr, "signer") {
		t.Fatalf("stderr %q, want the unchanged unsigned report", stderr)
	}
}

// TestCLIPlanSignFlagRefusals: -sign without -seal, or naming no file, is a
// usage error (exit 2), before anything is recorded.
func TestCLIPlanSignFlagRefusals(t *testing.T) {
	isolateXDGConfig(t)
	registerSealTask(t)
	k := runKeygen(t, t.TempDir())
	recipient, _ := genSealKeyPair(t)
	for name, args := range map[string][]string{
		"without -seal":   {"plan", "-sign", k.path, "cli_seal_task"},
		"with -stdout":    {"plan", "-stdout", "-sign", k.path, "cli_seal_task"},
		"with -redacted":  {"plan", "-redacted", "-sign", k.path, "cli_seal_task"},
		"empty path":      {"plan", "-seal", "-recipient", recipient, "-sign", "", "cli_seal_task"},
		"empty with -for": {"plan", "-seal", "-recipient", recipient, "-for", "x", "-sign=", "cli_seal_task"},
	} {
		t.Run(name, func(t *testing.T) {
			code, stdout, stderr := runGonfCaptureStdout(t, args...)
			if code != 2 || stdout != "" || !strings.Contains(stderr, "plan: ") {
				t.Fatalf("exit %d, stdout %q, stderr %q; want exit 2", code, stdout, stderr)
			}
		})
	}
}

// TestCLIPlanSignLoadRefusals: a signer file LoadSigner refuses stops the
// run before anything is recorded or written, and the refusal never
// includes the file's content.
func TestCLIPlanSignLoadRefusals(t *testing.T) {
	isolateXDGConfig(t)
	work := registerSealTask(t)
	k := runKeygen(t, t.TempDir())
	recipient, _ := genSealKeyPair(t)
	secretLine := "GONF-SIGNER-SECRET-ED25519 " + k.secret
	cases := map[string]struct {
		content string
		mode    os.FileMode
		want    string
	}{
		"group-readable": {secretLine + "\n", 0o640, "readable or writable by group or other"},
		"malformed line": {secretLine + " trailing\n", 0o600, "malformed"},
		"two keys":       {secretLine + "\n" + secretLine + "\n", 0o600, "exactly one"},
		"public line":    {k.publicLine + "\n", 0o600, "expected a GONF-SIGNER-SECRET-ED25519 line"},
		"missing":        {"", 0, "does not exist"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "signer")
			if tc.mode != 0 {
				writeModeFile(t, path, tc.content, tc.mode)
			}
			dir := filepath.Join(work, "out-"+strings.ReplaceAll(name, " ", "-"))
			code, stdout, stderr := runGonfCaptureStdout(t, "plan", "-o", dir, "-seal", "-recipient", recipient, "-sign", path, "cli_seal_task")
			if code != 1 || stdout != "" || !strings.HasPrefix(stderr, "plan: -sign refused: ") || !strings.Contains(stderr, tc.want) {
				t.Fatalf("exit %d, stdout %q, stderr %q; want exit 1 naming %q", code, stdout, stderr, tc.want)
			}
			if _, err := os.Lstat(dir); !os.IsNotExist(err) {
				t.Fatalf("output directory exists after a refused -sign (%v)", err)
			}
			requireNoSecret(t, k.secret, stderr)
			if strings.Contains(stderr, strings.Fields(k.publicLine)[1]) {
				t.Fatalf("refusal echoes key content: %q", stderr)
			}
		})
	}
}

// writeModeFile writes content to path with exactly mode (chmod after the
// write, since os.WriteFile's mode is narrowed by umask).
func writeModeFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}
