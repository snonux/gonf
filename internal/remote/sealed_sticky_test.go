package remote

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/snonux/gonf/internal/logger"
	"github.com/snonux/gonf/internal/privilege"
	"github.com/snonux/gonf/internal/testutil"
	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/plan/seal"
)

// sshCall is one captured SSHRunner invocation.
type sshCall struct {
	remote string // the remote command (argv's last element)
	argv   []string
	stdin  []byte
}

// sealedPushHarness fakes SSH (capturing every call's argv and stdin), the
// remote release probe and the Push bootstrap, so the production sealed
// sticky path (task 062's refusal is gone since task 0g2) can be exercised
// end to end. release is the release the remote reports; probed records
// each probe's privilege context.
type sealedPushHarness struct {
	mu     sync.Mutex
	calls  []sshCall
	probed []ProbeContext
	sshErr func(remote string) error
	// recorder observes the Push bootstrap (EnsureRemoteGonf's self-heal).
	recorder *deliveryRecorder
}

func installSealedPushHarness(t *testing.T, release string) *sealedPushHarness {
	t.Helper()
	h := &sealedPushHarness{}
	h.recorder = installDeliveryRecorder(t) // probes current, bootstrap observed, SSH restored on cleanup
	oldRelease := defaultPusher.ReleaseVersionProber
	t.Cleanup(func() { defaultPusher.ReleaseVersionProber = oldRelease })
	defaultPusher.ReleaseVersionProber = func(_ context.Context, _ PushTarget, pc ProbeContext) (string, error) {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.probed = append(h.probed, pc)
		return release, nil
	}
	SSHRunner = func(_ context.Context, stdin io.Reader, argv []string) error {
		data, _ := io.ReadAll(stdin)
		h.mu.Lock()
		defer h.mu.Unlock()
		remote := argv[len(argv)-1]
		h.calls = append(h.calls, sshCall{remote: remote, argv: argv, stdin: data})
		if h.sshErr != nil {
			return h.sshErr(remote)
		}
		return nil
	}
	return h
}

func (h *sealedPushHarness) snapshot() []sshCall {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]sshCall(nil), h.calls...)
}

// sealedPush runs a sensitive-elevated-blob push (sensitiveStickyOps) under
// h and returns its calls: blob upload, unprivileged chunk, elevated chunk,
// sticky removal.
func sealedPush(t *testing.T, h *sealedPushHarness) ([]sshCall, error) {
	t.Helper()
	ops, mem := sensitiveStickyOps(t, true)
	target := PushTarget{Host: "h.example", Privilege: privilege.Sudo}
	err := pushToHost(context.Background(), target, "p", ops, mem)
	return h.snapshot(), err
}

// keyLineOf returns the raw key line text of a GONF-PUSH/2 frame.
func keyLineOf(t *testing.T, frame []byte) string {
	t.Helper()
	lines := strings.SplitN(string(frame), "\n", 3)
	if len(lines) < 3 || lines[0] != "GONF-PUSH/2" || !strings.HasPrefix(lines[1], "key AGE-SECRET-KEY-PQ-1") {
		t.Fatalf("not a keyed GONF-PUSH/2 frame: %q", frame[:min(len(frame), 24)])
	}
	return strings.TrimPrefix(lines[1], "key ")
}

// End to end through the real upload and chunk-stream code: the sensitive
// ref is uploaded only sealed (the destination's sticky dir never holds its
// plaintext), the unprivileged chunk and the upload keep GONF-PUSH/1, only
// the elevated chunk gets a GONF-PUSH/2 frame, and its key opens the sealed
// ref back to exactly the blob. The key is on no argv and in no log line.
func TestToHostSealsSensitiveStickyBlobRoundTrip(t *testing.T) {
	logs := testutil.CaptureLog(t, logger.LevelDebug)
	h := installSealedPushHarness(t, sealedStickyMinRelease)
	calls, err := sealedPush(t, h)
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if len(calls) != 4 || !strings.HasPrefix(calls[3].remote, "rm -rf ") {
		t.Fatalf("calls = %d, want upload, 2 chunks, rm: %+v", len(calls), calls)
	}
	upload, unpriv, elevated := calls[0], calls[1], calls[2]
	for name, c := range map[string]sshCall{"upload": upload, "unprivileged chunk": unpriv} {
		if !bytes.HasPrefix(c.stdin, []byte("GONF-PUSH/1\n")) {
			t.Fatalf("%s frame is not GONF-PUSH/1: %q", name, c.stdin[:min(len(c.stdin), 16)])
		}
	}
	if !strings.Contains(elevated.remote, "sudo") {
		t.Fatalf("third call %q is not the elevated chunk", elevated.remote)
	}
	keyLine := keyLineOf(t, elevated.stdin)
	stickyDir := t.TempDir()
	if _, err := plan.DecodePush(bytes.NewReader(upload.stdin), stickyDir); err != nil {
		t.Fatalf("destination decode of the upload: %v", err)
	}
	assertNoPlaintextOnDisk(t, stickyDir, "blob-content")
	payload, err := plan.DecodePushWithKey(bytes.NewReader(elevated.stdin), "")
	if err != nil || payload.Key == nil {
		t.Fatalf("elevated chunk decode = %v (key %v)", err, payload != nil && payload.Key != nil)
	}
	if got := openSealedRef(t, stickyDir, "blobs/demo.txt", parseKey(t, payload.Key)); got != "blob-content" {
		t.Fatalf("decrypted sealed ref = %q, want the blob", got)
	}
	assertKeyNowhere(t, keyLine, calls, logs())
}

// assertNoPlaintextOnDisk fails when any file under dir contains secret.
func assertNoPlaintextOnDisk(t *testing.T, dir, secret string) {
	t.Helper()
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, _ := os.ReadFile(path)
		if bytes.Contains(data, []byte(secret)) {
			t.Fatalf("sticky dir file %s holds the plaintext", path)
		}
		return nil
	})
}

// parseKey is the destination's step after DecodePushWithKey: the frame's
// opaque key parsed back into a seal.Identity.
func parseKey(t *testing.T, key *plan.PushKey) seal.Identity {
	t.Helper()
	id, err := seal.ParseEphemeral(key.Line())
	if err != nil {
		t.Fatalf("ParseEphemeral: %v", err)
	}
	return id
}

// openSealedRef decrypts ref's sealed stream from stickyDir with key and
// returns the single file the archive holds.
func openSealedRef(t *testing.T, stickyDir, ref string, key seal.Identity) string {
	t.Helper()
	f, err := os.Open(filepath.Join(stickyDir, filepath.FromSlash(plan.SealedBlobPath(ref))))
	if err != nil {
		t.Fatalf("sealed ref missing: %v", err)
	}
	defer func() { _ = f.Close() }()
	r, err := seal.Open(f, []seal.Identity{key})
	if err != nil {
		t.Fatalf("open sealed ref: %v", err)
	}
	gz, err := gzip.NewReader(r)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	hdr, err := tr.Next()
	if err != nil || hdr.Name != ref {
		t.Fatalf("archive entry = %v, %v; want %s", hdr, err, ref)
	}
	data, _ := io.ReadAll(tr)
	return string(data)
}

// assertKeyNowhere fails when the key line appears on any argv, in any
// frame other than the elevated chunk's, or in the captured log output.
func assertKeyNowhere(t *testing.T, key string, calls []sshCall, logs string) {
	t.Helper()
	for i, c := range calls {
		if strings.Contains(strings.Join(c.argv, " "), key) || strings.Contains(strings.Join(c.argv, " "), "AGE-SECRET") {
			t.Fatalf("call %d argv carries the key", i)
		}
		if i != 2 && bytes.Contains(c.stdin, []byte(key)) {
			t.Fatalf("call %d stdin carries the key; only the elevated chunk may", i)
		}
	}
	if strings.Contains(logs, key) || strings.Contains(logs, "AGE-SECRET") {
		t.Fatal("the key reached the log")
	}
}

// Each push generates a fresh key: two pushes of the same plan never send
// the same identity.
func TestToHostSealedStickyKeyIsPerPush(t *testing.T) {
	h := installSealedPushHarness(t, sealedStickyMinRelease)
	first, err := sealedPush(t, h)
	if err != nil {
		t.Fatal(err)
	}
	all, err := sealedPush(t, h)
	if err != nil {
		t.Fatal(err)
	}
	if a, b := keyLineOf(t, first[2].stdin), keyLineOf(t, all[len(first)+2].stdin); a == b {
		t.Fatal("two pushes reused one ephemeral key")
	}
}

// Old-remote compatibility: the bootstrap step (EnsureRemoteGonf's
// self-heal) runs first, and a remote still below the sealed-sticky floor
// after it is refused before any SSH traffic: no blob, sealed or not, is
// uploaded and no chunk runs. The probe runs in the elevated context,
// which decodes the keyed frame. The remote reports v0.16.6, the last
// release without 0g2's decrypt support: pinned explicitly rather than
// taken from internal.Version, which has reached the floor since v0.17.0.
func TestToHostSealedStickyRefusesRemoteBelowFloor(t *testing.T) {
	h := installSealedPushHarness(t, "0.16.6")
	calls, err := sealedPush(t, h)
	if err == nil || !strings.Contains(err.Error(), "sealed sticky-dir blobs") {
		t.Fatalf("push = %v, want the sealed-sticky capability refusal", err)
	}
	if len(calls) != 0 {
		t.Fatalf("refused push sent %d SSH calls", len(calls))
	}
	if len(h.probed) != 1 || h.probed[0] != ProbeElevated {
		t.Fatalf("probe contexts = %v, want one ProbeElevated probe", h.probed)
	}
	if n := h.recorder.bootstraps.Load(); n != 1 {
		t.Fatalf("bootstraps = %d, want the self-heal step to run before the floor check", n)
	}
}

// A failing keyed chunk's error carries neither the key nor the frame.
func TestToHostSealedStickyChunkFailureDoesNotLeakKey(t *testing.T) {
	h := installSealedPushHarness(t, sealedStickyMinRelease)
	h.sshErr = func(remote string) error {
		if strings.Contains(remote, "sudo") {
			return errors.New("remote apply failed")
		}
		return nil
	}
	calls, err := sealedPush(t, h)
	if err == nil || !strings.Contains(err.Error(), "remote apply failed") {
		t.Fatalf("push = %v, want the chunk failure", err)
	}
	var key string
	for _, c := range calls {
		if strings.Contains(c.remote, "sudo") {
			key = keyLineOf(t, c.stdin)
		}
	}
	if key == "" || strings.Contains(err.Error(), key) || strings.Contains(err.Error(), "AGE-SECRET") {
		t.Fatal("chunk failure error missing, or it leaks the key (error not echoed)")
	}
}

// A plan with nothing to seal generates no key and sends only GONF-PUSH/1
// frames, even with the refusal stepped past: the non-sealing path is
// unchanged on the wire, and needs no sealed-sticky capability probe.
func TestToHostWithoutSealedRefsStaysV1(t *testing.T) {
	h := installSealedPushHarness(t, "0.0.1")
	ops, mem := stickyOps(t)
	if err := pushToHost(context.Background(), PushTarget{Host: "h.example", Privilege: privilege.Sudo}, "p", ops, mem); err != nil {
		t.Fatalf("push: %v", err)
	}
	for i, c := range h.snapshot() {
		if len(c.stdin) > 0 && !bytes.HasPrefix(c.stdin, []byte("GONF-PUSH/1\n")) {
			t.Fatalf("call %d sent %q, want GONF-PUSH/1", i, c.stdin[:min(len(c.stdin), 16)])
		}
	}
	if len(h.probed) != 0 {
		t.Fatalf("non-sealing push probed the sealed-sticky capability: %v", h.probed)
	}
}

// RequireRemoteSealedSticky mirrors RequireRemoteRelayed: at the floor
// passes, one patch below refuses, a probe error refuses.
func TestRequireRemoteSealedStickyFloor(t *testing.T) {
	p := NewPusher()
	target := PushTarget{Host: "h.example"}
	for release, wantOK := range map[string]bool{sealedStickyMinRelease: true, "0.16.99": false, "9.0.0": true} {
		p.ReleaseVersionProber = func(context.Context, PushTarget, ProbeContext) (string, error) { return release, nil }
		if err := p.RequireRemoteSealedSticky(context.Background(), target, ProbeElevated); (err == nil) != wantOK {
			t.Fatalf("release %s: err = %v, want ok=%v", release, err, wantOK)
		}
	}
	p.ReleaseVersionProber = func(context.Context, PushTarget, ProbeContext) (string, error) { return "", errors.New("boom") }
	if err := p.RequireRemoteSealedSticky(context.Background(), target, ProbeElevated); err == nil {
		t.Fatal("probe error accepted")
	}
}

// The floor is derived from its literal (task wf2's drift guard) and is
// exactly v0.17.0, the release the user chose to ship task 0g2's
// destination side (task yg2). v0.16.6, the last release without 0g2, must
// stay below it, so no remote lacking the decrypt support can pass the gate.
func TestSealedStickyFloorIsReleaseShipping0g2(t *testing.T) {
	want, err := parseReleaseVersion(sealedStickyMinRelease)
	if err != nil || sealedStickyMinVersion != want {
		t.Fatalf("sealedStickyMinVersion = %v, want %v derived from %q (%v)", sealedStickyMinVersion, want, sealedStickyMinRelease, err)
	}
	if sealedStickyMinRelease != "0.17.0" {
		t.Fatalf("sealedStickyMinRelease = %q, want 0.17.0 (the release chosen to ship 0g2, task yg2)", sealedStickyMinRelease)
	}
	lastWithout0g2, err := parseReleaseVersion("0.16.6")
	if err != nil {
		t.Fatal(err)
	}
	if !releaseVersionLess(lastWithout0g2, sealedStickyMinVersion) {
		t.Fatalf("v0.16.6 (no 0g2) must stay below the floor %s", sealedStickyMinRelease)
	}
}
