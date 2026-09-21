package remote

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/snonux/gonf/internal/privilege"
	"github.com/snonux/gonf/plan"
)

// errFanoutBoom is a sentinel a fake SSH runner returns so the tests can
// check errors.Is reaches a per-host cause through the Fanout aggregate.
var errFanoutBoom = errors.New("boom")

// fanoutErrorFixture returns a one-op plan plus n targets/labels (h1..hn)
// whose ssh destination is "<label>.example", so a fake SSHRunner can tell
// hosts apart from the argv's destination element.
func fanoutErrorFixture(n int) ([]plan.Op, []PushTarget, []string) {
	ops := []plan.Op{
		{Op: plan.KindPlan, Version: plan.CurrentVersion, ID: "demo"},
		{Op: plan.KindFile, Path: "/tmp/out", Mode: "0600", ContentB64: "aGVsbG8K"},
	}
	targets := make([]PushTarget, n)
	labels := make([]string, n)
	for i := range n {
		labels[i] = fmt.Sprintf("h%d", i+1)
		targets[i] = PushTarget{Host: labels[i] + ".example", Privilege: privilege.None}
	}
	return ops, targets, labels
}

// installFanoutRunner swaps SSHRunner (and every remote gonf version probe)
// for the duration of the test.
func installFanoutRunner(t *testing.T, fn func(ctx context.Context, argv []string) error) {
	t.Helper()
	// AssumeRemoteGonfCurrent fakes every remote gonf probe (a superset of
	// AssumeRemotePlanCurrent's), so no probe can shell out to a real ssh
	// (refuseNetworkExecInTests would fail the test if one did).
	restoreProbe := AssumeRemoteGonfCurrent()
	old := SSHRunner
	t.Cleanup(func() {
		SSHRunner = old
		restoreProbe()
	})
	SSHRunner = func(ctx context.Context, stdin io.Reader, argv []string) error {
		_, _ = io.Copy(io.Discard, stdin)
		return fn(ctx, argv)
	}
}

// sshDest returns the destination element of a pushTarget ssh argv (the
// element right before the remote command).
func sshDest(argv []string) string { return argv[len(argv)-2] }

// Per-host failures keep their error chains through the Fanout aggregate:
// a typed plan refusal and a sentinel are both reachable with errors.As /
// errors.Is, while the "; "-joined, host-attributed, sorted message stays
// exactly what users saw before.
func TestFanoutPreservesPerHostErrorChains(t *testing.T) {
	dangling := &plan.DanglingDepError{Op: "file:/a", Dep: "file:/b"}
	installFanoutRunner(t, func(_ context.Context, argv []string) error {
		if strings.Contains(sshDest(argv), "h1.") {
			return fmt.Errorf("remote apply: %w", dangling)
		}
		return errFanoutBoom
	})
	ops, targets, labels := fanoutErrorFixture(2)

	err := Fanout(context.Background(), Delivery{Mode: Push, PlanID: "p", Ops: ops},
		Group{Name: "demo", Targets: targets, Labels: labels, Limit: 2, HostTimeout: 0})
	if err == nil {
		t.Fatal("expected an aggregate error")
	}
	want := `cluster "demo": h1: chunk 0 (elevate=false): remote apply: ` + dangling.Error() +
		`; h2: chunk 0 (elevate=false): boom`
	if err.Error() != want {
		t.Fatalf("message changed:\n got %q\nwant %q", err.Error(), want)
	}
	var refusal plan.Refusal
	if !errors.As(err, &refusal) || refusal.Reason() != dangling.Reason() {
		t.Fatalf("errors.As(plan.Refusal) failed through Fanout: %v", err)
	}
	var dep *plan.DanglingDepError
	if !errors.As(err, &dep) || dep != dangling {
		t.Fatalf("errors.As(*plan.DanglingDepError) failed through Fanout: %v", err)
	}
	if !errors.Is(err, errFanoutBoom) {
		t.Fatalf("errors.Is(sentinel) failed through Fanout: %v", err)
	}
}

// A per-host timeout keeps both its wording ("(host timeout after ...)") and
// the context.DeadlineExceeded cause.
func TestFanoutHostTimeoutKeepsDeadlineChain(t *testing.T) {
	installFanoutRunner(t, func(ctx context.Context, _ []string) error {
		<-ctx.Done()
		return ctx.Err()
	})
	ops, targets, labels := fanoutErrorFixture(1)

	err := Fanout(context.Background(), Delivery{Mode: Push, PlanID: "p", Ops: ops},
		Group{Name: "slow", Targets: targets, Labels: labels, Limit: 1, HostTimeout: 20 * time.Millisecond})
	want := `cluster "slow": h1: chunk 0 (elevate=false): context deadline exceeded (host timeout after 20ms)`
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("errors.Is(DeadlineExceeded) failed through Fanout: %v", err)
	}
}

// A canceled fan-out still reports the abort once (no host blamed) and the
// context.Canceled cause stays inspectable.
func TestFanoutCanceledContextReportsAbort(t *testing.T) {
	installFanoutRunner(t, func(ctx context.Context, _ []string) error {
		<-ctx.Done()
		return ctx.Err()
	})
	ops, targets, labels := fanoutErrorFixture(2)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := Fanout(ctx, Delivery{Mode: Push, PlanID: "p", Ops: ops},
		Group{Name: "gone", Targets: targets, Labels: labels, Limit: 2, HostTimeout: 0})
	want := `cluster "gone": aborted: chunk 0 (elevate=false): context canceled`
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("errors.Is(Canceled) failed through Fanout: %v", err)
	}
}

// The real SSHRunner wraps a context kill with BOTH causes: the context error
// (so the fan-out can tell an abort from a failure) and ssh's own exit error,
// with the message unchanged.
func TestSSHRunnerContextKillKeepsExitErrorChain(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := SSHRunner(ctx, nil, []string{"sleep", "5"})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want it to wrap context.DeadlineExceeded", err)
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("err = %v, want it to wrap the ssh *exec.ExitError too", err)
	}
	want := "context deadline exceeded (ssh killed by context: " + exitErr.Error() + ")"
	if err.Error() != want {
		t.Fatalf("message changed:\n got %q\nwant %q", err.Error(), want)
	}
}
