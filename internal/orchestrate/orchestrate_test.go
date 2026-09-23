package orchestrate

import (
	"bytes"
	"context"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/snonux/gonf/internal/inventory"
	"github.com/snonux/gonf/internal/remote"
	"github.com/snonux/gonf/internal/testutil"
	"github.com/snonux/gonf/plan"
)

// TestDeliverUnregisteredHostReturnsError confirms Deliver resolves each host name
// via internal/inventory itself (no api.HostRef needed) and fails without
// dialing out when a name is not registered.
func TestDeliverUnregisteredHostReturnsError(t *testing.T) {
	inventory.Reset()
	t.Cleanup(inventory.Reset)

	mem := plan.NewMemoryStore()
	err := Deliver(context.Background(), remote.Delivery{Mode: remote.Push, PlanID: "plan-id", Mem: mem},
		Group{Name: "grp", HostNames: []string{"ghost"}, Limit: 1, HostTimeout: remote.DefaultHostTimeout})
	if err == nil {
		t.Fatal("Deliver with an unregistered host: want error, got nil")
	}
	if !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("Deliver error = %q, want it to name the unregistered host", err.Error())
	}
}

// TestDeliverFansOutToEachRegisteredHost is a smoke test that Deliver resolves
// every host name to a target via inventory.PushTargetFor and fans the
// (empty, here) plan out to each one via remote.Fanout -- proving Deliver has
// everything it needs (inventory + remote) without any api-package
// dependency.
func TestDeliverFansOutToEachRegisteredHost(t *testing.T) {
	inventory.Reset()
	t.Cleanup(inventory.Reset)
	mustAddHost(t, "h1", "h1.example")
	mustAddHost(t, "h2", "h2.example")

	oldRunner := remote.SSHRunner
	restoreProbe := remote.AssumeRemotePlanCurrent()
	t.Cleanup(func() {
		remote.SSHRunner = oldRunner
		restoreProbe()
	})
	var calls atomic.Int32
	remote.SSHRunner = func(ctx context.Context, stdin io.Reader, argv []string) error {
		calls.Add(1)
		_, _ = io.Copy(io.Discard, stdin)
		return nil
	}

	mem := plan.NewMemoryStore()
	ops := []plan.Op{{Op: plan.KindEnsureDir, ID: "1", Path: "/tmp/orchestrate-test-dir"}}
	d := remote.Delivery{Mode: remote.Push, PlanID: "plan-id", Ops: ops, Mem: mem}
	g := Group{Name: "grp", HostNames: []string{"h1", "h2"}, Limit: 2, HostTimeout: remote.DefaultHostTimeout}
	if err := Deliver(context.Background(), d, g); err != nil {
		t.Fatal(err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("SSHRunner calls = %d, want 2", got)
	}
}

// TestDeliverModeDecidesBootstrap pins that Deliver forwards the Delivery's
// Mode unchanged to every host: a Push may bootstrap gonf on each host, a
// Preview never does and runs the strict remote preview instead.
func TestDeliverModeDecidesBootstrap(t *testing.T) {
	tests := []struct {
		mode           remote.Mode
		wantBootstraps int32
		wantCmd        string
	}{
		{remote.Push, 2, "gonf apply -"},
		{remote.Preview, 0, "gonf apply -n -strict-preview -"},
	}
	for _, tc := range tests {
		t.Run(tc.mode.String(), func(t *testing.T) {
			inventory.Reset()
			t.Cleanup(inventory.Reset)
			mustAddHost(t, "h1", "h1.example")
			mustAddHost(t, "h2", "h2.example")
			bootstraps, cmds := observeDelivery(t)

			ops := []plan.Op{{Op: plan.KindEnsureDir, ID: "1", Path: "/tmp/orchestrate-test-dir"}}
			d := remote.Delivery{Mode: tc.mode, PlanID: "plan-id", Ops: ops, Mem: plan.NewMemoryStore()}
			g := Group{Name: "grp", HostNames: []string{"h1", "h2"}, Limit: 2}
			if err := Deliver(context.Background(), d, g); err != nil {
				t.Fatal(err)
			}
			if got := bootstraps.Load(); got != tc.wantBootstraps {
				t.Fatalf("bootstraps = %d, want %d", got, tc.wantBootstraps)
			}
			if len(*cmds) != 2 || (*cmds)[0] != tc.wantCmd || (*cmds)[1] != tc.wantCmd {
				t.Fatalf("remote cmds = %q, want two %q", *cmds, tc.wantCmd)
			}
		})
	}
}

// observeDelivery fakes SSH and every remote gonf probe for the test and
// returns the Push-mode bootstrap counter plus the remote commands run. The
// commands are appended under a mutex (Fanout runs hosts concurrently) and
// read only after Deliver has returned.
func observeDelivery(t *testing.T) (*atomic.Int32, *[]string) {
	t.Helper()
	bootstraps := &atomic.Int32{}
	var mu sync.Mutex
	cmds := &[]string{}
	oldRunner := remote.SSHRunner
	restoreProbes := remote.AssumeRemoteGonfCurrent()
	restoreBootstrap := remote.ObserveBootstrapForTest(func(remote.PushTarget) { bootstraps.Add(1) })
	t.Cleanup(func() {
		remote.SSHRunner = oldRunner
		restoreBootstrap()
		restoreProbes()
	})
	remote.SSHRunner = func(_ context.Context, stdin io.Reader, argv []string) error {
		_, _ = io.Copy(io.Discard, stdin)
		mu.Lock()
		defer mu.Unlock()
		*cmds = append(*cmds, argv[len(argv)-1])
		return nil
	}
	return bootstraps, cmds
}

// Deliver forwards Group.Writer straight through to remote.Fanout as
// remote.Group.Writer: the fan-out's summary line lands in it instead of
// os.Stderr, the one seam api's groupRun (and, through it, every push/preview
// entry point) relies on to share output policy across single-host, cluster
// and fleet runs.
func TestDeliverWritesSummaryThroughGroupWriter(t *testing.T) {
	inventory.Reset()
	t.Cleanup(inventory.Reset)
	mustAddHost(t, "h1", "h1.example")
	mustAddHost(t, "h2", "h2.example")
	oldRunner := remote.SSHRunner
	restoreProbe := remote.AssumeRemotePlanCurrent()
	t.Cleanup(func() {
		remote.SSHRunner = oldRunner
		restoreProbe()
	})
	remote.SSHRunner = func(_ context.Context, stdin io.Reader, _ []string) error {
		_, _ = io.Copy(io.Discard, stdin)
		return nil
	}

	var buf bytes.Buffer
	ops := []plan.Op{{Op: plan.KindEnsureDir, ID: "1", Path: "/tmp/orchestrate-test-dir"}}
	d := remote.Delivery{Mode: remote.Push, PlanID: "plan-id", Ops: ops, Mem: plan.NewMemoryStore()}
	g := Group{Name: "grp", HostNames: []string{"h1", "h2"}, Limit: 2, Writer: &buf}
	stderr := testutil.CaptureStderr(t, func() {
		if err := Deliver(context.Background(), d, g); err != nil {
			t.Fatal(err)
		}
	})
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty: Group.Writer should have taken the summary instead", stderr)
	}
	want := "pushed plan-id (1 ops) to grp (2/2 hosts)\n"
	if buf.String() != want {
		t.Fatalf("buf = %q, want %q", buf.String(), want)
	}
}

// A zero-value Group (Writer unset, matching every production Deliver call
// before api's groupRun set it explicitly) still writes its summary to
// os.Stderr: the default stays byte-identical whether or not a caller opts
// into Group.Writer.
func TestDeliverNilWriterDefaultsToStderr(t *testing.T) {
	inventory.Reset()
	t.Cleanup(inventory.Reset)
	mustAddHost(t, "h1", "h1.example")
	oldRunner := remote.SSHRunner
	restoreProbe := remote.AssumeRemotePlanCurrent()
	t.Cleanup(func() {
		remote.SSHRunner = oldRunner
		restoreProbe()
	})
	remote.SSHRunner = func(_ context.Context, stdin io.Reader, _ []string) error {
		_, _ = io.Copy(io.Discard, stdin)
		return nil
	}

	ops := []plan.Op{{Op: plan.KindEnsureDir, ID: "1", Path: "/tmp/orchestrate-test-dir"}}
	d := remote.Delivery{Mode: remote.Push, PlanID: "plan-id", Ops: ops, Mem: plan.NewMemoryStore()}
	g := Group{Name: "grp", HostNames: []string{"h1"}, Limit: 1}
	stderr := testutil.CaptureStderr(t, func() {
		if err := Deliver(context.Background(), d, g); err != nil {
			t.Fatal(err)
		}
	})
	want := "pushed plan-id (1 ops) to grp (1/1 hosts)\n"
	if stderr != want {
		t.Fatalf("stderr = %q, want %q", stderr, want)
	}
}

// mustAddHost registers host name with sshHost in the inventory, failing the
// test when the registration is refused.
func mustAddHost(t *testing.T, name, sshHost string) {
	t.Helper()
	if _, err := inventory.AddHost(name, func(h *inventory.Host) error { h.SSHHost = sshHost; return nil }); err != nil {
		t.Fatal(err)
	}
}
