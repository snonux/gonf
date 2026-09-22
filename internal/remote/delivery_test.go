package remote

import (
	"context"
	"errors"
	"io"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/snonux/gonf/internal/privilege"
	"github.com/snonux/gonf/internal/testutil"
	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
)

// pushToHost is a push to one target: Delivery.ToHost in Push mode. The
// push tests use it where they once called the removed PushChunks wrapper.
func pushToHost(ctx context.Context, t PushTarget, planID string, ops []plan.Op, mem plan.BlobReader) error {
	return Delivery{Mode: Push, PlanID: planID, Ops: ops, Mem: mem}.ToHost(ctx, t)
}

// previewToHost is a strict preview of one target: Delivery.ToHost in
// Preview mode. The preview tests use it where they once called the removed
// PreviewChunks wrapper.
func previewToHost(ctx context.Context, t PushTarget, planID string, ops []plan.Op, mem plan.BlobReader) error {
	return Delivery{Mode: Preview, PlanID: planID, Ops: ops, Mem: mem}.ToHost(ctx, t)
}

// deliveryOps is a minimal one-chunk, unprivileged plan.
func deliveryOps() []plan.Op {
	return []plan.Op{
		{Op: plan.KindPlan, Version: plan.CurrentVersion, ID: "demo"},
		{Op: plan.KindFile, Path: "/tmp/out", Mode: "0600", ContentB64: "aGVsbG8K"},
	}
}

// deliveryRecorder fakes SSH and every remote gonf probe, observes the
// Push-mode bootstrap step, and records the remote commands it saw.
type deliveryRecorder struct {
	mu         sync.Mutex
	remoteCmds []string
	bootstraps atomic.Int32
}

func installDeliveryRecorder(t *testing.T) *deliveryRecorder {
	t.Helper()
	r := &deliveryRecorder{}
	restoreProbes := AssumeRemoteGonfCurrent()
	restoreBootstrap := ObserveBootstrapForTest(func(PushTarget) { r.bootstraps.Add(1) })
	old := SSHRunner
	t.Cleanup(func() {
		SSHRunner = old
		restoreBootstrap()
		restoreProbes()
	})
	SSHRunner = func(_ context.Context, stdin io.Reader, argv []string) error {
		_, _ = io.Copy(io.Discard, stdin)
		r.mu.Lock()
		defer r.mu.Unlock()
		r.remoteCmds = append(r.remoteCmds, argv[len(argv)-1])
		return nil
	}
	return r
}

func (r *deliveryRecorder) cmds() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.remoteCmds...)
}

// The zero Mode is invalid: a Delivery built without a Mode must never fall
// back to Push (which may install gonf), so ToHost and Fanout both refuse it
// before any SSH traffic.
func TestDeliveryZeroModeRefusedBeforeSSH(t *testing.T) {
	r := installDeliveryRecorder(t)
	target := PushTarget{Host: "h.example", Privilege: privilege.None}
	d := Delivery{PlanID: "demo", Ops: deliveryOps()}

	if err := d.ToHost(context.Background(), target); err == nil || !strings.Contains(err.Error(), "invalid delivery mode") {
		t.Fatalf("ToHost(zero Mode) = %v, want invalid delivery mode", err)
	}
	g := Group{Name: "c", Targets: []PushTarget{target}, Labels: []string{"h"}, Limit: 1}
	if err := Fanout(context.Background(), d, g); err == nil || !strings.Contains(err.Error(), "invalid delivery mode") {
		t.Fatalf("Fanout(zero Mode) = %v, want invalid delivery mode", err)
	}
	if got := r.cmds(); len(got) != 0 || r.bootstraps.Load() != 0 {
		t.Fatalf("zero Mode reached the remote: cmds=%v bootstraps=%d", got, r.bootstraps.Load())
	}
}

// applyStdinArg is the single place that turns a Mode (and push -n) into the
// remote "gonf apply" arguments. Preview is strict whether or not resource
// dry-run is set; Push is a plain dry run only under push -n.
func TestModeApplyStdinArg(t *testing.T) {
	old := resource.DryRun()
	t.Cleanup(func() { resource.SetDryRun(old) })
	tests := []struct {
		mode   Mode
		dryRun bool
		want   string
	}{
		{Push, false, "-"},
		{Push, true, "-n -"},
		{Preview, false, "-n -strict-preview -"},
		{Preview, true, "-n -strict-preview -"},
	}
	for _, tc := range tests {
		resource.SetDryRun(tc.dryRun)
		if got := tc.mode.applyStdinArg(); got != tc.want {
			t.Errorf("%s dryRun=%v: applyStdinArg = %q, want %q", tc.mode, tc.dryRun, got, tc.want)
		}
	}
}

// Mode names and verbs feed diagnostics and the summary lines.
func TestModeStringAndVerb(t *testing.T) {
	if Push.String() != "push" || Preview.String() != "preview" || Mode(0).String() != "Mode(0)" {
		t.Fatalf("String: %q %q %q", Push, Preview, Mode(0))
	}
	if Push.Verb() != "pushed" || Preview.Verb() != "previewed" {
		t.Fatalf("Verb: %q %q", Push.Verb(), Preview.Verb())
	}
}

// The Mode carried in the Delivery alone decides, per host, whether the
// fan-out may bootstrap gonf (Push) or must not (Preview), and which remote
// apply command runs. The stderr summary line (whose verb comes from the
// Mode, and whose "to <cluster>" wording is the same for both) is pinned too.
func TestFanoutModeDecidesBootstrap(t *testing.T) {
	tests := []struct {
		mode           Mode
		wantBootstraps int32
		wantCmd        string
		wantSummary    string
	}{
		{Push, 2, "gonf apply -", "pushed p (2 ops) to c (2/2 hosts)\n"},
		{Preview, 0, "gonf apply -n -strict-preview -", "previewed p (2 ops) to c (2/2 hosts)\n"},
	}
	for _, tc := range tests {
		t.Run(tc.mode.String(), func(t *testing.T) {
			r := installDeliveryRecorder(t)
			_, targets, labels := fanoutErrorFixture(2)
			d := Delivery{Mode: tc.mode, PlanID: "p", Ops: deliveryOps()}
			g := Group{Name: "c", Targets: targets, Labels: labels, Limit: 2}
			var err error
			stderr := testutil.CaptureStderr(t, func() { err = Fanout(context.Background(), d, g) })
			if err != nil {
				t.Fatalf("Fanout: %v", err)
			}
			if stderr != tc.wantSummary {
				t.Fatalf("summary = %q, want %q", stderr, tc.wantSummary)
			}
			if got := r.bootstraps.Load(); got != tc.wantBootstraps {
				t.Fatalf("bootstraps = %d, want %d", got, tc.wantBootstraps)
			}
			cmds := r.cmds()
			if len(cmds) != 2 {
				t.Fatalf("remote cmds = %v, want one per host", cmds)
			}
			for _, c := range cmds {
				if c != tc.wantCmd {
					t.Fatalf("remote cmd = %q, want %q", c, tc.wantCmd)
				}
			}
		})
	}
}

// A Push-mode ToHost reaches the bootstrap step, while a Preview Delivery to
// the same target does not.
func TestToHostModeDecidesBootstrap(t *testing.T) {
	r := installDeliveryRecorder(t)
	target := PushTarget{Host: "h.example", Privilege: privilege.None}
	if err := pushToHost(context.Background(), target, "demo", deliveryOps(), nil); err != nil {
		t.Fatalf("push: %v", err)
	}
	if got := r.bootstraps.Load(); got != 1 {
		t.Fatalf("push bootstraps = %d, want 1", got)
	}
	if err := previewToHost(context.Background(), target, "demo", deliveryOps(), nil); err != nil {
		t.Fatalf("preview: %v", err)
	}
	if got := r.bootstraps.Load(); got != 1 {
		t.Fatalf("preview bootstrapped: bootstraps = %d, want still 1", got)
	}
}

// stickyOps is a two-chunk plan (one unprivileged, one elevated op) and a
// blob store with one blob: together they make ToHost use a sticky dir.
func stickyOps(t *testing.T) ([]plan.Op, *plan.MemoryStore) {
	t.Helper()
	ops := []plan.Op{
		{Op: plan.KindPlan, Version: plan.CurrentVersion, ID: "p"},
		{Op: plan.KindFile, Path: "/tmp/unpriv-out", Mode: "0600", ContentB64: "aGVsbG8K"},
		{Op: plan.KindFile, Path: "/tmp/priv-out", Mode: "0600", ContentB64: "aGVsbG8K", Elevate: true},
	}
	mem := plan.NewMemoryStore()
	if _, err := mem.WriteFile("demo.txt", []byte("blob-content")); err != nil {
		t.Fatal(err)
	}
	return ops, mem
}

// Fanout gives every host its own plan ID (<planID>-<label>, see forHost),
// and with it its own sticky dir: two hosts of one fan-out never share (or
// wipe) each other's staged blobs, and no host uses the bare plan ID.
func TestFanoutSuffixesStickyDirPerHost(t *testing.T) {
	r := installDeliveryRecorder(t)
	ops, mem := stickyOps(t)
	targets := []PushTarget{
		{Host: "h1.example", Privilege: privilege.Sudo},
		{Host: "h2.example", Privilege: privilege.Sudo},
	}
	d := Delivery{Mode: Push, PlanID: "p", Ops: ops, Mem: mem}
	g := Group{Name: "c", Targets: targets, Labels: []string{"h1", "h2"}, Limit: 2}
	if err := Fanout(context.Background(), d, g); err != nil {
		t.Fatalf("Fanout: %v", err)
	}
	stickyDir := regexp.MustCompile(`/tmp/gonf-apply-sticky-[A-Za-z0-9_-]+`)
	seen := map[string]int{}
	for _, c := range r.cmds() {
		for _, dir := range stickyDir.FindAllString(c, -1) {
			seen[dir]++
		}
	}
	if len(seen) != 2 {
		t.Fatalf("sticky dirs = %v, want exactly one per host: %v", seen, r.cmds())
	}
	// Per host: blob upload + two chunk applies use -apply-dir, plus rm -rf.
	for _, dir := range []string{"/tmp/gonf-apply-sticky-p-h1", "/tmp/gonf-apply-sticky-p-h2"} {
		if seen[dir] != 4 {
			t.Fatalf("%s used %d times, want 4 (upload, 2 chunks, cleanup): %v", dir, seen[dir], r.cmds())
		}
	}
}

// When the Push-mode bootstrap installs gonf at a new path, every remote
// command after it — the sticky blob upload and each chunk's apply — must
// run that fresh binary, not the PATH "gonf" the pre-flight built them with.
func TestToHostRebuildsCommandsAfterInstall(t *testing.T) {
	r := installDeliveryRecorder(t)
	old := ensureRuntime
	t.Cleanup(func() { ensureRuntime = old })
	ensureRuntime = func(context.Context, PushTarget) (string, error) { return "/opt/fresh/gonf", nil }

	ops, mem := stickyOps(t)
	target := PushTarget{Host: "h.example", Privilege: privilege.Sudo}
	if err := pushToHost(context.Background(), target, "p", ops, mem); err != nil {
		t.Fatalf("push: %v", err)
	}
	applies := 0
	for _, c := range r.cmds() {
		if strings.HasPrefix(c, "rm -rf ") {
			continue
		}
		applies++
		if !strings.Contains(c, "/opt/fresh/gonf apply ") {
			t.Fatalf("remote cmd %q does not run the freshly installed binary", c)
		}
	}
	if applies != 3 {
		t.Fatalf("apply sessions = %d, want 3 (blob upload + 2 chunks): %v", applies, r.cmds())
	}
}

// A failing Push-mode bootstrap (EnsureRemoteGonf) fails the delivery with
// its own error, before any chunk's SSH apply session is opened.
func TestToHostBootstrapFailureSendsNoChunk(t *testing.T) {
	r := installDeliveryRecorder(t)
	old := ensureRuntime
	t.Cleanup(func() { ensureRuntime = old })
	boom := errors.New("bootstrap boom")
	ensureRuntime = func(context.Context, PushTarget) (string, error) { return "", boom }

	target := PushTarget{Host: "h.example", Privilege: privilege.None}
	if err := pushToHost(context.Background(), target, "demo", deliveryOps(), nil); !errors.Is(err, boom) {
		t.Fatalf("push = %v, want the bootstrap error", err)
	}
	if got := r.cmds(); len(got) != 0 {
		t.Fatalf("remote cmds after a failed bootstrap = %v, want none", got)
	}
}

// A failing rebuild of the remote commands after the bootstrap installed
// gonf at a new path fails the delivery (wrapping the rebuild error and
// naming the path), before any blob upload or chunk apply is sent.
func TestToHostRebuildFailureSendsNoChunk(t *testing.T) {
	r := installDeliveryRecorder(t)
	oldEnsure, oldRebuild := ensureRuntime, rebuildRemoteCmds
	t.Cleanup(func() { ensureRuntime, rebuildRemoteCmds = oldEnsure, oldRebuild })
	ensureRuntime = func(context.Context, PushTarget) (string, error) { return "/opt/fresh/gonf", nil }
	boom := errors.New("rebuild boom")
	rebuildRemoteCmds = func([]plan.Chunk, PushTarget, string, Mode) ([]string, error) { return nil, boom }

	ops, mem := stickyOps(t)
	target := PushTarget{Host: "h.example", Privilege: privilege.Sudo}
	err := pushToHost(context.Background(), target, "p", ops, mem)
	if !errors.Is(err, boom) || !strings.Contains(err.Error(), "/opt/fresh/gonf") {
		t.Fatalf("push = %v, want the wrapped rebuild error naming the installed path", err)
	}
	if got := r.cmds(); len(got) != 0 {
		t.Fatalf("remote cmds after a failed rebuild = %v, want none", got)
	}
}
