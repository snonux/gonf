package cli

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"filippo.io/age"

	"github.com/snonux/gonf/api"
	"github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/internal/testutil"
	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/plan/seal"
	"github.com/snonux/gonf/resource"
)

// runGonfCaptureStdout runs `gonf <args...>` like runGonf, but also captures
// what the run wrote to os.Stdout: cliApplyFile's own "applied .../decrypted
// and applied ..." summary goes there (fmt.Printf, matching the unsealed
// file-apply path it sits next to), while the stdin path's equivalent
// summary goes to stderr (eprintf, matching the unsealed stdin path) —
// runGonf alone is enough for every stdin-path assertion in this file, but
// not for a file-path one.
func runGonfCaptureStdout(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldOut := os.Stdout
	os.Stdout = w
	oldArgs := os.Args
	os.Args = append([]string{"gonf"}, args...)
	t.Cleanup(func() { os.Args = oldArgs })
	stderr = testutil.CaptureStderr(t, func() { code = CLI() })
	_ = w.Close()
	os.Stdout = oldOut
	raw, err := io.ReadAll(r)
	_ = r.Close()
	if err != nil {
		t.Fatal(err)
	}
	return code, string(raw), stderr
}

// sealedKeyPair returns a fresh hybrid identity line (AGE-SECRET-KEY-PQ-1...)
// and its matching recipient line (age1pq1...), generated through
// filippo.io/age directly (internal/cli is outside plan/seal, so it cannot
// reach into that package's unexported Recipient/Identity fields the way
// plan/seal's own in-package tests do — it goes through the same public
// text encoding an operator's `age-keygen -pq` output would use instead).
func sealedKeyPair(t *testing.T) (identityLine, recipientLine string) {
	t.Helper()
	id, err := age.GenerateHybridIdentity()
	if err != nil {
		t.Fatalf("generate hybrid identity: %v", err)
	}
	return id.String(), id.Recipient().String()
}

// writeSealedIdentityFile writes line as a 0600 identity file owned by the
// test process (the mode LoadIdentities requires) and returns its path.
func writeSealedIdentityFile(t *testing.T, dir, name, line string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(line+"\n"), 0o600); err != nil {
		t.Fatalf("write identity file: %v", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// sealFrame seals plaintext (a GONF-PUSH/1 frame or bare JSONL) to the given
// recipient lines and returns the sealed bytes, through the same
// plan/seal.Seal 2b2's `gonf plan -seal` itself calls.
func sealFrame(t *testing.T, plaintext []byte, recipientLines []string) []byte {
	t.Helper()
	recipients, err := seal.ParseRecipients(recipientLines)
	if err != nil {
		t.Fatalf("ParseRecipients: %v", err)
	}
	var buf bytes.Buffer
	wc, err := seal.Seal(&buf, recipients)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if _, err := wc.Write(plaintext); err != nil {
		t.Fatalf("write sealed plaintext: %v", err)
	}
	if err := wc.Close(); err != nil {
		t.Fatalf("close sealed writer: %v", err)
	}
	return buf.Bytes()
}

// writeSealedPlanFile writes sealed as a plan.age file (mode 0600, matching
// what gonf plan -o dir -seal itself writes via plan.WritePrivateFile) and
// returns its path.
func writeSealedPlanFile(t *testing.T, dir string, sealed []byte) string {
	t.Helper()
	path := filepath.Join(dir, "plan.age")
	if err := os.WriteFile(path, sealed, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// touchOp returns a two-op plan (header + a File op writing "hi\n" to
// target) and the raw GONF-PUSH/1 frame bytes for it (no blobs).
func touchFramePush(t *testing.T, id, target string) []byte {
	t.Helper()
	ops := []plan.Op{
		{Op: plan.KindPlan, Version: plan.CurrentVersion, ID: id},
		{Op: plan.KindFile, Path: target, Mode: "0600", Payload: plan.FilePayload{ContentB64: "aGkK"}}, // "hi\n"
	}
	var buf bytes.Buffer
	if err := plan.EncodePush(&buf, ops, nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// blobFramePush returns a GONF-PUSH/1 frame whose single File op is
// blob-backed (content "blob content\n" packaged through a MemoryStore),
// exercising DecodePush's blob-unpacking side effect the way a sealed apply
// with blobs does.
func blobFramePush(t *testing.T, id, target string) []byte {
	t.Helper()
	return blobFramePushContent(t, id, target, []byte("blob content\n"))
}

// blobFramePushContent is blobFramePush with caller-supplied content, for a
// test that needs to control its size precisely (e.g. spanning more than
// one age payload segment).
func blobFramePushContent(t *testing.T, id, target string, content []byte) []byte {
	t.Helper()
	mem := plan.NewMemoryStore()
	ref, err := mem.WriteFile("secret", content)
	if err != nil {
		t.Fatal(err)
	}
	ops := []plan.Op{
		{Op: plan.KindPlan, Version: plan.CurrentVersion, ID: id},
		{Op: plan.KindFile, Path: target, Mode: "0600", Blob: ref},
	}
	var buf bytes.Buffer
	if err := plan.EncodePush(&buf, ops, mem); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// isolateSealedStagingRoot points TMPDIR at a scratch directory so a sealed
// apply's staging root ($TMPDIR/gonf-apply/<uid>) never touches the real
// one, and returns it for post-apply assertions (e.g. "no leftover
// sealed-run-* directory").
func isolateSealedStagingRoot(t *testing.T) string {
	t.Helper()
	t.Setenv("TMPDIR", t.TempDir())
	root, err := plan.ApplyStagingRoot()
	if err != nil {
		t.Fatal(err)
	}
	return root
}

// requireNoLeftoverSealedRunDirs fails if root holds any sealed-run-*
// entry: the invariant every sealed apply (success or failure) must leave
// true (docs/plan-encryption.md, "Failure handling": "run dir removed").
func requireNoLeftoverSealedRunDirs(t *testing.T, root string) {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return
		}
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "sealed-run-") {
			t.Fatalf("leftover sealed-run directory %s under %s", e.Name(), root)
		}
	}
}

func TestCLIApplySealedFileNoBlobsInMemoryNoRunDir(t *testing.T) {
	root := isolateSealedStagingRoot(t)
	identityLine, recipientLine := sealedKeyPair(t)
	dir := t.TempDir()
	identityPath := writeSealedIdentityFile(t, dir, "identity", identityLine)
	target := filepath.Join(dir, "out.txt")
	sealed := sealFrame(t, touchFramePush(t, "sealed-no-blobs", target), []string{recipientLine})
	planPath := writeSealedPlanFile(t, dir, sealed)

	code, stdout, stderr := runGonfCaptureStdout(t, "apply", "-identity", identityPath, planPath)
	if code != 0 {
		t.Fatalf("apply exit %d, stderr %q", code, stderr)
	}
	if !strings.Contains(stdout, "decrypted and applied "+planPath+" (2 ops)") {
		t.Fatalf("stdout %q missing expected 'decrypted and applied' summary", stdout)
	}
	combined := strings.ToLower(stdout + stderr)
	if strings.Contains(combined, "verified") || strings.Contains(combined, "authenticated") {
		t.Fatalf("output must never claim verification/authentication: stdout %q stderr %q", stdout, stderr)
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != "hi\n" {
		t.Fatalf("target content = %q, %v", got, err)
	}
	// The common no-blobs case must never touch disk for staging: the
	// staging root itself may exist (ApplyStagingRoot creates it), but it
	// must hold no run directory of either kind.
	entries, err := os.ReadDir(root)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	for _, e := range entries {
		t.Fatalf("no-blobs sealed apply must create no run dir, found %s", e.Name())
	}
}

func TestCLIApplySealedFileWithBlobsUsesAndCleansSealedRunDir(t *testing.T) {
	root := isolateSealedStagingRoot(t)
	identityLine, recipientLine := sealedKeyPair(t)
	dir := t.TempDir()
	identityPath := writeSealedIdentityFile(t, dir, "identity", identityLine)
	target := filepath.Join(dir, "out.txt")
	sealed := sealFrame(t, blobFramePush(t, "sealed-with-blobs", target), []string{recipientLine})
	planPath := writeSealedPlanFile(t, dir, sealed)

	code, stderr := runGonf(t, "apply", "-identity", identityPath, planPath)
	if code != 0 {
		t.Fatalf("apply exit %d, stderr %q", code, stderr)
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != "blob content\n" {
		t.Fatalf("target content = %q, %v", got, err)
	}
	requireNoLeftoverSealedRunDirs(t, root)
}

// TestCLIApplySealedIdentityBadModeErrorIsNotDoubleWrapped pins task de2's
// finding (a): an identity file's ErrIdentityMode refusal used to reach
// stderr with the "apply: " prefix doubled — loadSealedIdentities wrapped
// seal.LoadIdentities' own error with its own "apply: %w", and the single
// call site here (cliApplyFile -> cliApplySealedFile) wrapped it again with
// "apply: %v" — producing the confusing "apply: apply: plan/seal: identity
// file ...: identity file is readable or writable by group or other"
// (100 Go Mistakes #52). This exercises the exact repro: a 0640 identity
// file (group-readable) passed to `gonf apply -identity`, both through the
// file-apply path (cliApplyFile) and the stdin-apply path
// (cliApplyStdin/cliApplySealedStdin), since both call the same
// loadSealedIdentities through decryptAndDecodeSealedPush.
func TestCLIApplySealedIdentityBadModeErrorIsNotDoubleWrapped(t *testing.T) {
	identityLine, recipientLine := sealedKeyPair(t)
	dir := t.TempDir()
	identityPath := filepath.Join(dir, "identity")
	if err := os.WriteFile(identityPath, []byte(identityLine+"\n"), 0o640); err != nil {
		t.Fatalf("write identity file: %v", err)
	}
	if err := os.Chmod(identityPath, 0o640); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "out.txt")
	sealed := sealFrame(t, touchFramePush(t, "sealed-bad-mode", target), []string{recipientLine})
	planPath := writeSealedPlanFile(t, dir, sealed)

	code, stderr := runGonf(t, "apply", "-identity", identityPath, planPath)
	if code == 0 {
		t.Fatalf("a group-readable identity file must be refused; stderr %q", stderr)
	}
	if strings.Contains(stderr, "apply: apply:") {
		t.Fatalf("stderr %q still double-wraps the apply: prefix", stderr)
	}
	if !strings.Contains(stderr, "apply: plan/seal: identity file") {
		t.Fatalf("stderr %q missing the single, correctly-prefixed error", stderr)
	}
	if !strings.Contains(stderr, "identity file is readable or writable by group or other") {
		t.Fatalf("stderr %q missing the ErrIdentityMode text", stderr)
	}

	// The stdin path (cliApplySealedStdin) shares loadSealedIdentities with
	// the file path above through decryptAndDecodeSealedPush, so it must
	// show the same single-prefix behavior.
	oldStdin := os.Stdin
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdin = r
	t.Cleanup(func() {
		os.Stdin = oldStdin
		_ = r.Close()
	})
	go func() {
		_, _ = w.Write(sealed)
		_ = w.Close()
	}()

	stdinCode, stdinStderr := runGonf(t, "apply", "-identity", identityPath, "-")
	if stdinCode == 0 {
		t.Fatalf("stdin apply with a group-readable identity file must be refused; stderr %q", stdinStderr)
	}
	if strings.Contains(stdinStderr, "apply: apply:") {
		t.Fatalf("stdin stderr %q still double-wraps the apply: prefix", stdinStderr)
	}
}

func TestCLIApplySealedStdinNoBlobs(t *testing.T) {
	isolateSealedStagingRoot(t)
	identityLine, recipientLine := sealedKeyPair(t)
	dir := t.TempDir()
	identityPath := writeSealedIdentityFile(t, dir, "identity", identityLine)
	target := filepath.Join(dir, "out.txt")
	sealed := sealFrame(t, touchFramePush(t, "sealed-stdin", target), []string{recipientLine})

	oldStdin := os.Stdin
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdin = r
	t.Cleanup(func() {
		os.Stdin = oldStdin
		_ = r.Close()
	})
	go func() {
		_, _ = w.Write(sealed)
		_ = w.Close()
	}()

	code, stderr := runGonf(t, "apply", "-identity", identityPath, "-")
	if code != 0 {
		t.Fatalf("apply - exit %d, stderr %q", code, stderr)
	}
	if !strings.Contains(stderr, "decrypted and applied stdin (2 ops)") {
		t.Fatalf("stderr %q missing expected stdin summary", stderr)
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != "hi\n" {
		t.Fatalf("target content = %q, %v", got, err)
	}
}

func TestCLIApplySealedStdinWithBlobsReportsBlobsSuffix(t *testing.T) {
	root := isolateSealedStagingRoot(t)
	identityLine, recipientLine := sealedKeyPair(t)
	dir := t.TempDir()
	identityPath := writeSealedIdentityFile(t, dir, "identity", identityLine)
	target := filepath.Join(dir, "out.txt")
	sealed := sealFrame(t, blobFramePush(t, "sealed-stdin-blobs", target), []string{recipientLine})

	oldStdin := os.Stdin
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdin = r
	t.Cleanup(func() {
		os.Stdin = oldStdin
		_ = r.Close()
	})
	go func() {
		_, _ = w.Write(sealed)
		_ = w.Close()
	}()

	code, stderr := runGonf(t, "apply", "-identity", identityPath, "-")
	if code != 0 {
		t.Fatalf("apply - exit %d, stderr %q", code, stderr)
	}
	if !strings.Contains(stderr, "decrypted and applied stdin+blobs (2 ops)") {
		t.Fatalf("stderr %q missing expected stdin+blobs summary", stderr)
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != "blob content\n" {
		t.Fatalf("target content = %q, %v", got, err)
	}
	requireNoLeftoverSealedRunDirs(t, root)
}

// TestCLIApplySealedTruncatedAppliesNothing is the highest-value security
// property here: age authenticates only its final segment at EOF, so a
// stream cut short must fail before a single op runs, and — since blobs
// were never reached — no run directory is ever created.
func TestCLIApplySealedTruncatedAppliesNothing(t *testing.T) {
	root := isolateSealedStagingRoot(t)
	identityLine, recipientLine := sealedKeyPair(t)
	dir := t.TempDir()
	identityPath := writeSealedIdentityFile(t, dir, "identity", identityLine)
	target := filepath.Join(dir, "out.txt")
	sealed := sealFrame(t, blobFramePush(t, "sealed-truncated", target), []string{recipientLine})
	truncated := sealed[:len(sealed)-16]
	planPath := writeSealedPlanFile(t, dir, truncated)

	code, stderr := runGonf(t, "apply", "-identity", identityPath, planPath)
	if code == 0 {
		t.Fatalf("truncated sealed input must not apply; stderr %q", stderr)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("truncated sealed input must apply nothing, target exists: %v", err)
	}
	requireNoLeftoverSealedRunDirs(t, root)
}

// TestCLIApplySealedMultiSegmentTruncationAppliesNothing is the sharpest
// form of the "read to EOF before applying" invariant: age's payload is
// chunked into 64 KiB segments, each individually authenticated as it is
// read, so a small (single-segment) payload can never distinguish "read to
// EOF and check the error" from "use whatever came back before an error" —
// an incomplete first-and-only segment releases zero plaintext bytes either
// way. A payload spanning SEVERAL segments is different: the earlier
// segments genuinely decrypt and authenticate individually, and only the
// truncated final one fails — so code that read the stream to EOF but
// forgot to check ReadAll's error would still see (and could act on) those
// earlier segments' real plaintext. This test uses a blob large enough to
// span multiple segments, truncated only in the last one, and is the test
// this task's self-review specifically re-ran against a deliberately
// reintroduced "ignore ReadAll's error" regression to confirm it — unlike
// the smaller truncation/bit-flip tests above — actually fails without the
// fix (see the task's ask annotation for the revert-and-retest record).
func TestCLIApplySealedMultiSegmentTruncationAppliesNothing(t *testing.T) {
	root := isolateSealedStagingRoot(t)
	identityLine, recipientLine := sealedKeyPair(t)
	dir := t.TempDir()
	identityPath := writeSealedIdentityFile(t, dir, "identity", identityLine)
	target := filepath.Join(dir, "out.txt")
	// Random, not repeated, content: EncodePush gzips the blob tar, and
	// gzip would collapse a repeated byte down to a few dozen bytes,
	// defeating the point of a multi-segment sealed payload.
	big := make([]byte, 200*1024) // several 64 KiB age segments, post-gzip
	if _, err := rand.Read(big); err != nil {
		t.Fatal(err)
	}
	sealed := sealFrame(t, blobFramePushContent(t, "sealed-multiseg-truncated", target, big), []string{recipientLine})
	if len(sealed) < 128*1024 {
		t.Fatalf("sealed payload only %d bytes; too small to guarantee multiple segments", len(sealed))
	}
	truncated := sealed[:len(sealed)-64] // cut into the final segment only
	planPath := writeSealedPlanFile(t, dir, truncated)

	code, stderr := runGonf(t, "apply", "-identity", identityPath, planPath)
	if code == 0 {
		t.Fatalf("a multi-segment sealed input truncated in its final segment must not apply; stderr %q", stderr)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("truncated multi-segment sealed input must apply nothing, target exists: %v", err)
	}
	requireNoLeftoverSealedRunDirs(t, root)
}

// TestCLIApplySealedBitFlippedAppliesNothing: a single corrupted byte
// anywhere in the sealed stream must fail authentication and apply nothing.
func TestCLIApplySealedBitFlippedAppliesNothing(t *testing.T) {
	root := isolateSealedStagingRoot(t)
	identityLine, recipientLine := sealedKeyPair(t)
	dir := t.TempDir()
	identityPath := writeSealedIdentityFile(t, dir, "identity", identityLine)
	target := filepath.Join(dir, "out.txt")
	sealed := sealFrame(t, touchFramePush(t, "sealed-bitflip", target), []string{recipientLine})
	flipped := append([]byte(nil), sealed...)
	mid := len(flipped) / 2
	flipped[mid] ^= 0xFF
	planPath := writeSealedPlanFile(t, dir, flipped)

	code, stderr := runGonf(t, "apply", "-identity", identityPath, planPath)
	if code == 0 {
		t.Fatalf("bit-flipped sealed input must not apply; stderr %q", stderr)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("bit-flipped sealed input must apply nothing, target exists: %v", err)
	}
	requireNoLeftoverSealedRunDirs(t, root)
}

// TestCLIApplySealedWrongIdentityAppliesNothing: an identity that never
// matches any recipient the file was sealed to must fail, applying nothing.
func TestCLIApplySealedWrongIdentityAppliesNothing(t *testing.T) {
	root := isolateSealedStagingRoot(t)
	_, recipientLine := sealedKeyPair(t)
	wrongIdentityLine, _ := sealedKeyPair(t)
	dir := t.TempDir()
	identityPath := writeSealedIdentityFile(t, dir, "identity", wrongIdentityLine)
	target := filepath.Join(dir, "out.txt")
	sealed := sealFrame(t, touchFramePush(t, "sealed-wrong-identity", target), []string{recipientLine})
	planPath := writeSealedPlanFile(t, dir, sealed)

	code, stderr := runGonf(t, "apply", "-identity", identityPath, planPath)
	if code == 0 {
		t.Fatalf("wrong-identity sealed input must not apply; stderr %q", stderr)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("wrong-identity sealed input must apply nothing, target exists: %v", err)
	}
	requireNoLeftoverSealedRunDirs(t, root)
}

// TestCLIApplySealedCorruptedBlobsFramePlaintextCleansUpRunDir exercises the
// other failure shape: the AEAD authenticates fine (a genuine, uncorrupted
// seal of SOME plaintext), but that plaintext's own blob tar/gzip section is
// malformed, so DecodePush fails only after plan.NewSealedApplyRunDir
// already created a run directory for it. That directory must still be
// removed before cliApply returns.
func TestCLIApplySealedCorruptedBlobsFramePlaintextCleansUpRunDir(t *testing.T) {
	root := isolateSealedStagingRoot(t)
	identityLine, recipientLine := sealedKeyPair(t)
	dir := t.TempDir()
	identityPath := writeSealedIdentityFile(t, dir, "identity", identityLine)
	target := filepath.Join(dir, "out.txt")
	frame := blobFramePush(t, "sealed-corrupt-blobs", target)

	prefix := []byte("GONF-PUSH/1\nblobs 1\n")
	if !bytes.HasPrefix(frame, prefix) {
		t.Fatalf("unexpected frame shape, prefix %q not found in %q", prefix, frame[:len(prefix)])
	}
	idx := len(prefix) + 20
	if idx >= len(frame) {
		t.Skip("frame too short to corrupt past the blobs-phase prefix")
	}
	corrupted := append([]byte(nil), frame...)
	corrupted[idx] ^= 0xFF

	sealed := sealFrame(t, corrupted, []string{recipientLine})
	planPath := writeSealedPlanFile(t, dir, sealed)

	code, stderr := runGonf(t, "apply", "-identity", identityPath, planPath)
	if code == 0 {
		t.Fatalf("a corrupted blobs-phase plaintext must not apply; stderr %q", stderr)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("corrupted blobs-phase input must apply nothing, target exists: %v", err)
	}
	requireNoLeftoverSealedRunDirs(t, root)
}

// TestCLIApplyRootRequiresIdentityForSealedInput pins the "no default
// identity for root" rule (docs/plan-encryption.md, "Keys"): euid is faked
// via the package's geteuid seam (internal/privilege's geteuid is the
// established pattern this module already uses for the same problem — a
// test cannot actually become root to exercise this).
func TestCLIApplyRootRequiresIdentityForSealedInput(t *testing.T) {
	isolateSealedStagingRoot(t)
	_, recipientLine := sealedKeyPair(t)
	dir := t.TempDir()
	target := filepath.Join(dir, "out.txt")
	sealed := sealFrame(t, touchFramePush(t, "sealed-root", target), []string{recipientLine})
	planPath := writeSealedPlanFile(t, dir, sealed)

	origEuid := geteuid
	geteuid = func() int { return 0 }
	t.Cleanup(func() { geteuid = origEuid })

	code, stderr := runGonf(t, "apply", planPath)
	if code == 0 {
		t.Fatalf("root without -identity must be refused; stderr %q", stderr)
	}
	if !strings.Contains(stderr, "-identity is required") {
		t.Fatalf("stderr %q must explain that -identity is required for root", stderr)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("refused root apply must apply nothing, target exists: %v", err)
	}
}

// TestCLIApplyRootWithIdentitySucceeds: once -identity is given explicitly,
// a faked-root invocation applies normally (the refusal is specifically
// about guessing a default, not about root applying sealed plans at all).
func TestCLIApplyRootWithIdentitySucceeds(t *testing.T) {
	isolateSealedStagingRoot(t)
	identityLine, recipientLine := sealedKeyPair(t)
	dir := t.TempDir()
	identityPath := writeSealedIdentityFile(t, dir, "identity", identityLine)
	target := filepath.Join(dir, "out.txt")
	sealed := sealFrame(t, touchFramePush(t, "sealed-root-ok", target), []string{recipientLine})
	planPath := writeSealedPlanFile(t, dir, sealed)

	origEuid := geteuid
	geteuid = func() int { return 0 }
	t.Cleanup(func() { geteuid = origEuid })

	code, stderr := runGonf(t, "apply", "-identity", identityPath, planPath)
	if code != 0 {
		t.Fatalf("root with -identity should apply; exit %d, stderr %q", code, stderr)
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != "hi\n" {
		t.Fatalf("target content = %q, %v", got, err)
	}
}

// TestCLIApplySealedDefaultIdentityPath: without -identity, a non-root
// invocation reads ${XDG_CONFIG_HOME}/gonf/identity.
func TestCLIApplySealedDefaultIdentityPath(t *testing.T) {
	isolateSealedStagingRoot(t)
	identityLine, recipientLine := sealedKeyPair(t)
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	gonfConfigDir := filepath.Join(xdg, "gonf")
	if err := os.MkdirAll(gonfConfigDir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeSealedIdentityFile(t, gonfConfigDir, "identity", identityLine)

	dir := t.TempDir()
	target := filepath.Join(dir, "out.txt")
	sealed := sealFrame(t, touchFramePush(t, "sealed-default-identity", target), []string{recipientLine})
	planPath := writeSealedPlanFile(t, dir, sealed)

	code, stderr := runGonf(t, "apply", planPath)
	if code != 0 {
		t.Fatalf("apply with default identity path exit %d, stderr %q", code, stderr)
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != "hi\n" {
		t.Fatalf("target content = %q, %v", got, err)
	}
}

func TestCLIApplySealedStdinRefusesStrictPreview(t *testing.T) {
	isolateSealedStagingRoot(t)
	_, recipientLine := sealedKeyPair(t)
	sealed := sealFrame(t, touchFramePush(t, "sealed-strict-preview", filepath.Join(t.TempDir(), "x")), []string{recipientLine})

	oldStdin := os.Stdin
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdin = r
	t.Cleanup(func() {
		os.Stdin = oldStdin
		_ = r.Close()
	})
	go func() {
		_, _ = w.Write(sealed)
		_ = w.Close()
	}()

	code, stderr := runGonf(t, "apply", "-strict-preview", "-")
	if code != 2 {
		t.Fatalf("apply -strict-preview - (sealed) exit %d, want 2; stderr %q", code, stderr)
	}
	if !strings.Contains(stderr, "-strict-preview cannot be combined with a sealed plan.age stream") {
		t.Fatalf("stderr %q missing expected refusal", stderr)
	}
}

func TestCLIApplySealedStdinRefusesApplyDir(t *testing.T) {
	isolateSealedStagingRoot(t)
	_, recipientLine := sealedKeyPair(t)
	sealed := sealFrame(t, touchFramePush(t, "sealed-apply-dir", filepath.Join(t.TempDir(), "x")), []string{recipientLine})

	oldStdin := os.Stdin
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdin = r
	t.Cleanup(func() {
		os.Stdin = oldStdin
		_ = r.Close()
	})
	go func() {
		_, _ = w.Write(sealed)
		_ = w.Close()
	}()

	code, stderr := runGonf(t, "apply", "-apply-dir", t.TempDir(), "-")
	if code != 2 {
		t.Fatalf("apply -apply-dir - (sealed) exit %d, want 2; stderr %q", code, stderr)
	}
	if !strings.Contains(stderr, "-apply-dir cannot be combined with a sealed plan.age stream") {
		t.Fatalf("stderr %q missing expected refusal", stderr)
	}
}

func TestCLISealedVersionFlag(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldOut := os.Stdout
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = oldOut })

	setOSArgs(t, "gonf", "-sealed-version")
	code := CLI()
	_ = w.Close()
	os.Stdout = oldOut
	raw, err := io.ReadAll(r)
	_ = r.Close()
	if err != nil {
		t.Fatal(err)
	}
	if code != 0 {
		t.Fatalf("-sealed-version exit %d", code)
	}
	if strings.TrimSpace(string(raw)) != "1" {
		t.Fatalf("-sealed-version printed %q, want \"1\"", raw)
	}
}

// TestCLIApplyExternalAgeDecryptThenPlainApplyEquivalence proves, at the
// library level (age -d | gonf apply - equivalence, per the task's own
// suggested cross-check), that decrypting a sealed plan OUTSIDE gonf
// (here: through the same plan/seal.Open a real `age -d -i key` run is
// interoperability-tested against elsewhere — plan/seal's own 1b2 self-review
// cross-checked plan/seal against the real age CLI binary) and piping the
// resulting plaintext into the ordinary, unsealed `gonf apply -` path
// applies exactly the same ops as `gonf apply -identity ... plan.age`
// applying the sealed file directly.
func TestCLIApplyExternalAgeDecryptThenPlainApplyEquivalence(t *testing.T) {
	isolateSealedStagingRoot(t)
	identityLine, recipientLine := sealedKeyPair(t)
	dir := t.TempDir()
	identityPath := writeSealedIdentityFile(t, dir, "identity", identityLine)

	sealedTarget := filepath.Join(dir, "sealed-out.txt")
	plaintextTarget := filepath.Join(dir, "plaintext-out.txt")
	sealedFrame := touchFramePush(t, "equiv-sealed", sealedTarget)
	plaintextFrame := touchFramePush(t, "equiv-plaintext", plaintextTarget)
	sealed := sealFrame(t, sealedFrame, []string{recipientLine})
	planPath := writeSealedPlanFile(t, dir, sealed)

	// Path A: gonf apply -identity ... plan.age (decrypts internally).
	codeA, stderrA := runGonf(t, "apply", "-identity", identityPath, planPath)
	if codeA != 0 {
		t.Fatalf("sealed apply exit %d, stderr %q", codeA, stderrA)
	}

	// Path B: "age -d | gonf apply -" equivalent — decrypt independently
	// (library-level stand-in for shelling to the real age binary, which
	// may not be installed in every CI environment) and pipe the resulting
	// plaintext GONF-PUSH/1 frame into the ordinary unsealed stdin path.
	identities, err := seal.LoadIdentities(identityPath)
	if err != nil {
		t.Fatalf("LoadIdentities: %v", err)
	}
	// Re-seal the SECOND (plaintext-target) frame so both paths apply
	// independently prepared, but structurally identical, sealed input —
	// only the decrypt-then-decode route differs.
	sealedForB := sealFrame(t, plaintextFrame, []string{recipientLine})
	dr, err := seal.Open(bytes.NewReader(sealedForB), identities)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	decrypted, err := io.ReadAll(dr)
	if err != nil {
		t.Fatalf("read decrypted plaintext: %v", err)
	}
	if !bytes.Equal(decrypted, plaintextFrame) {
		t.Fatalf("independently decrypted plaintext does not match the original frame")
	}

	oldStdin := os.Stdin
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdin = r
	t.Cleanup(func() {
		os.Stdin = oldStdin
		_ = r.Close()
	})
	go func() {
		_, _ = w.Write(decrypted)
		_ = w.Close()
	}()
	codeB, stderrB := runGonf(t, "apply", "-")
	if codeB != 0 {
		t.Fatalf("plain apply of externally-decrypted plaintext exit %d, stderr %q", codeB, stderrB)
	}

	gotSealed, err := os.ReadFile(sealedTarget)
	if err != nil {
		t.Fatalf("sealed-path target: %v", err)
	}
	gotPlain, err := os.ReadFile(plaintextTarget)
	if err != nil {
		t.Fatalf("plaintext-path target: %v", err)
	}
	if string(gotSealed) != string(gotPlain) {
		t.Fatalf("sealed apply and decrypt-then-plain-apply produced different content: %q vs %q", gotSealed, gotPlain)
	}
}

// TestCLIPlanSealThenApplyIdentityInterop is the write-side/read-side
// integration smoke test: `gonf plan -o dir -seal -recipient ...` (task
// 2b2) followed by `gonf apply -identity ... dir/plan.age` (this task,
// 3b2) against a real registered task, through the full CLI() entry point
// both times — not a hand-built frame sealed by the test's own seal.Seal
// call, the way the rest of this file's tests deliberately isolate the
// apply side. Proves the two halves of w82 phase 1 actually interoperate.
func TestCLIPlanSealThenApplyIdentityInterop(t *testing.T) {
	isolateSealedStagingRoot(t)
	identityLine, recipientLine := sealedKeyPair(t)
	dir := t.TempDir()
	identityPath := writeSealedIdentityFile(t, dir, "identity", identityLine)

	api.ResetTasks()
	resource.ResetRepository()
	target := filepath.Join(dir, "out.txt")
	api.Task("cli_seal_apply_interop", "", func() {
		api.File(target, options.WithContent("sealed and applied\n"))
	})

	planDir := filepath.Join(dir, "planout")
	sealCode, sealStderr := runGonf(t, "plan", "-o", planDir, "-seal", "-recipient", recipientLine, "cli_seal_apply_interop")
	if sealCode != 0 {
		t.Fatalf("plan -seal exit %d, stderr %q", sealCode, sealStderr)
	}
	planPath := filepath.Join(planDir, "plan.age")
	if _, err := os.Stat(planPath); err != nil {
		t.Fatalf("plan.age was not written: %v", err)
	}
	if _, err := os.Stat(filepath.Join(planDir, "plan.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("gonf plan -seal must never write a plaintext plan.jsonl alongside plan.age: %v", err)
	}

	applyCode, applyStderr := runGonf(t, "apply", "-identity", identityPath, planPath)
	if applyCode != 0 {
		t.Fatalf("apply -identity exit %d, stderr %q", applyCode, applyStderr)
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != "sealed and applied\n" {
		t.Fatalf("target content = %q, %v", got, err)
	}
}

// TestReadSealedFrameCapsOutput is task be2's regression test for the
// memory-amplification DoS at the outer sealed-frame layer:
// decryptAndDecodeSealedPush used to io.ReadAll the fully-decrypted stream
// with no bound at all. It uses readSealedFrame's max parameter (not the
// real maxSealedFrameBytes constant) so the cap mechanism is proven at a
// small, fast scale rather than by actually decrypting and reading hundreds
// of megabytes in a test — the same "artificially low test-only cap"
// approach plan.TestMaybeGunzipCapsDecompressedOutput uses for the inner
// decompression cap this frame-level cap complements.
func TestReadSealedFrameCapsOutput(t *testing.T) {
	over := bytes.Repeat([]byte{0}, 128) // one byte over the test cap below
	const testMax = 127
	data, err := readSealedFrame(bytes.NewReader(over), testMax)
	if data != nil {
		t.Fatalf("readSealedFrame over cap returned %d bytes, want nil", len(data))
	}
	if !errors.Is(err, errSealedFrameTooLarge) {
		t.Fatalf("readSealedFrame over cap error = %v, want errSealedFrameTooLarge", err)
	}
}

// TestReadSealedFrameCapsOutputBoundsMemory is
// TestReadSealedFrameCapsOutput's "prove it, don't just assert the error"
// companion, mirroring plan.TestMaybeGunzipCapsDecompressedOutputBoundsMemory:
// it samples runtime.MemStats.TotalAlloc around a call whose SOURCE reader
// could supply many megabytes, but whose test-only cap is tiny, and asserts
// the call did not allocate anywhere near the source's true size —
// mathematically guaranteed by construction (readSealedFrame wraps dr in
// io.LimitReader(dr, max+1) before io.ReadAll, so it is never asked to read
// more than max+1 bytes regardless of how much dr could otherwise supply),
// and this test is the empirical check that the wiring actually behaves
// that way.
func TestReadSealedFrameCapsOutputBoundsMemory(t *testing.T) {
	const sourceSize = 5 << 20 // 5 MiB the reader could supply if unbounded
	source := bytes.Repeat([]byte{0}, sourceSize)
	const testMax = 64 << 10 // artificially low test-only cap

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	data, err := readSealedFrame(bytes.NewReader(source), testMax)
	runtime.ReadMemStats(&after)

	if data != nil || !errors.Is(err, errSealedFrameTooLarge) {
		t.Fatalf("readSealedFrame(source, %d) = (%d bytes, %v), want (nil, errSealedFrameTooLarge)", testMax, len(data), err)
	}
	const allocBound = 2 << 20 // 2 MiB: far below sourceSize, comfortably above cap+overhead
	if delta := after.TotalAlloc - before.TotalAlloc; delta > allocBound {
		t.Fatalf("readSealedFrame(source, %d) allocated %d bytes, want under %d (source is %d bytes)",
			testMax, delta, allocBound, sourceSize)
	}
}

// TestReadSealedFrameWithinCapUnaffected pins that a read well under the
// cap returns exactly what the reader held, unchanged from before this
// task's fix.
func TestReadSealedFrameWithinCapUnaffected(t *testing.T) {
	want := []byte("a small, legitimate decrypted frame\n")
	data, err := readSealedFrame(bytes.NewReader(want), maxSealedFrameBytes)
	if err != nil {
		t.Fatalf("readSealedFrame within cap: %v", err)
	}
	if !bytes.Equal(data, want) {
		t.Fatalf("readSealedFrame within cap = %q, want %q", data, want)
	}
}

// TestCLIApplySealedLargeLegitimatePlanSucceeds is task be2's "the fix must
// not break normal usage" regression: a legitimate plan with many sizable
// ops (docs/plan.md and this task's own maxSealedFrameBytes/
// plan.MaxDecompressedPushPlan doc comments both note "plans with many
// blob-backed ops can run to tens of MB") must still apply successfully,
// well within both of this task's caps, through the REAL constants — unlike
// TestReadSealedFrameCapsOutput/TestMaybeGunzipCapsDecompressedOutput
// (plan/pushwire_test.go), which deliberately use small test-only caps to
// prove the mechanism without decompressing hundreds of MB. Content is
// inline (ContentB64), not blob-backed, so this exercises the plan
// SECTION's own gzip decompression path (plan.MaxDecompressedPushPlan) —
// the one this task's fix bounds — rather than the separately-streamed,
// unaffected blobs phase.
func TestCLIApplySealedLargeLegitimatePlanSucceeds(t *testing.T) {
	root := isolateSealedStagingRoot(t)
	identityLine, recipientLine := sealedKeyPair(t)
	dir := t.TempDir()
	identityPath := writeSealedIdentityFile(t, dir, "identity", identityLine)

	const numOps = 20
	const opContentSize = 400 << 10 // 400 KiB per op, inline (under plan.MaxInlineContent's 512 KiB blob threshold)
	ops := []plan.Op{{Op: plan.KindPlan, Version: plan.CurrentVersion, ID: "sealed-large-legit"}}
	targets := make([]string, numOps)
	contents := make([][]byte, numOps)
	for i := range numOps {
		content := make([]byte, opContentSize)
		if _, err := rand.Read(content); err != nil {
			t.Fatal(err)
		}
		contents[i] = content
		targets[i] = filepath.Join(dir, fmt.Sprintf("large-%d.bin", i))
		ops = append(ops, plan.Op{
			Op:      plan.KindFile,
			Path:    targets[i],
			Mode:    "0600",
			Payload: plan.FilePayload{ContentB64: base64.StdEncoding.EncodeToString(content)},
		})
	}
	var buf bytes.Buffer
	if err := plan.EncodePush(&buf, ops, nil); err != nil {
		t.Fatal(err)
	}
	frame := buf.Bytes()

	sealed := sealFrame(t, frame, []string{recipientLine})
	if len(sealed) > maxSealedFrameBytes || len(sealed) > plan.MaxDecompressedPushPlan {
		t.Fatalf("test construction error: sealed frame %d bytes already exceeds a cap, not a meaningful legitimate-plan check", len(sealed))
	}
	planPath := writeSealedPlanFile(t, dir, sealed)

	code, stderr := runGonf(t, "apply", "-identity", identityPath, planPath)
	if code != 0 {
		t.Fatalf("legitimate large sealed plan (%d ops, %d bytes sealed) apply exit %d, stderr %q",
			numOps, len(sealed), code, stderr)
	}
	for i, target := range targets {
		got, err := os.ReadFile(target)
		if err != nil {
			t.Fatalf("target %d (%s): %v", i, target, err)
		}
		if !bytes.Equal(got, contents[i]) {
			t.Fatalf("target %d (%s) content mismatch", i, target)
		}
	}
	requireNoLeftoverSealedRunDirs(t, root)
}
