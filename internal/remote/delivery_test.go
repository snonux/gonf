package remote

import (
	"context"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/snonux/gonf/internal/privilege"
	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
)

// previewChunks is a strict preview of one target: Delivery.ToHost in
// Preview mode. The preview tests use it where they once called the removed
// PreviewChunks wrapper.
func previewChunks(ctx context.Context, t PushTarget, planID string, ops []plan.Op, mem plan.BlobReader) error {
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
// apply command runs.
func TestFanoutModeDecidesBootstrap(t *testing.T) {
	tests := []struct {
		mode           Mode
		wantBootstraps int32
		wantCmd        string
	}{
		{Push, 2, "gonf apply -"},
		{Preview, 0, "gonf apply -n -strict-preview -"},
	}
	for _, tc := range tests {
		t.Run(tc.mode.String(), func(t *testing.T) {
			r := installDeliveryRecorder(t)
			_, targets, labels := fanoutErrorFixture(2)
			d := Delivery{Mode: tc.mode, PlanID: "p", Ops: deliveryOps()}
			g := Group{Name: "c", Targets: targets, Labels: labels, Limit: 2}
			if err := Fanout(context.Background(), d, g); err != nil {
				t.Fatalf("Fanout: %v", err)
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

// PushChunks stays the Push-mode single-target entry point: it reaches the
// bootstrap step, while a Preview Delivery to the same target does not.
func TestPushChunksIsPushModeToHost(t *testing.T) {
	r := installDeliveryRecorder(t)
	target := PushTarget{Host: "h.example", Privilege: privilege.None}
	if err := PushChunks(context.Background(), target, "demo", deliveryOps(), nil); err != nil {
		t.Fatalf("PushChunks: %v", err)
	}
	if got := r.bootstraps.Load(); got != 1 {
		t.Fatalf("PushChunks bootstraps = %d, want 1", got)
	}
	if err := previewChunks(context.Background(), target, "demo", deliveryOps(), nil); err != nil {
		t.Fatalf("preview: %v", err)
	}
	if got := r.bootstraps.Load(); got != 1 {
		t.Fatalf("preview bootstrapped: bootstraps = %d, want still 1", got)
	}
}
