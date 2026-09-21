package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/snonux/gonf/internal/remote"
	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
)

// errFanoutBoom is a sentinel the fake SSH runner returns for hosts that do
// not return a typed refusal.
var errFanoutBoom = errors.New("boom")

// fanoutDangling is the typed refusal the fake SSH runner returns for hosts
// whose ssh destination contains "refuse".
var fanoutDangling = &plan.DanglingDepError{Op: "file:/a", Dep: "file:/b"}

// setupFanoutErrors registers task "fanout_err", clusters "ca" (host
// refuse1 → typed refusal) and "cb" (host boom1 → sentinel), and fleet "fe"
// over both. It installs a fake SSH runner (and fakes every remote gonf
// version probe, so nothing shells out to a real ssh). The runner ignores
// ctx unless block is set, in which case it waits for ctx to end and returns
// ctx.Err() (for timeout/cancellation cases).
func setupFanoutErrors(t *testing.T, block bool) {
	t.Helper()
	ResetInventory()
	ResetTasks()
	resource.ResetRepository()
	Task("fanout_err", "", func() {})
	ca := Cluster("ca", Host("refuse1", WithSSHHost("refuse1.example")))
	cb := Cluster("cb", Host("boom1", WithSSHHost("boom1.example")))
	Fleet("fe", ca, cb)

	old := remote.SSHRunner
	restoreProbe := remote.AssumeRemoteGonfCurrent()
	t.Cleanup(func() {
		remote.SSHRunner = old
		restoreProbe()
	})
	remote.SSHRunner = func(ctx context.Context, stdin io.Reader, argv []string) error {
		_, _ = io.Copy(io.Discard, stdin)
		if block {
			<-ctx.Done()
			return ctx.Err()
		}
		if strings.Contains(argv[len(argv)-2], "refuse") {
			return fmt.Errorf("remote apply: %w", fanoutDangling)
		}
		return errFanoutBoom
	}
}

// assertRefusalChain checks that a typed refusal is reachable through err.
func assertRefusalChain(t *testing.T, err error) {
	t.Helper()
	var refusal plan.Refusal
	if !errors.As(err, &refusal) || refusal.Reason() != fanoutDangling.Reason() {
		t.Fatalf("errors.As(plan.Refusal) failed: %v", err)
	}
	var dep *plan.DanglingDepError
	if !errors.As(err, &dep) || dep != fanoutDangling {
		t.Fatalf("errors.As(*plan.DanglingDepError) failed: %v", err)
	}
}

// A cluster push's per-host refusal stays reachable with errors.As, and the
// message is unchanged.
func TestPushClusterRunKeepsRefusalChain(t *testing.T) {
	setupFanoutErrors(t, false)
	err := PushClusterRun(context.Background(), "ca", "", 0, remote.DefaultHostTimeout, "fanout_err")
	want := `cluster "ca": refuse1: chunk 0 (elevate=false): remote apply: ` + fanoutDangling.Error()
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v\nwant %q", err, want)
	}
	assertRefusalChain(t, err)
}

// A fleet push joins its cluster groups' errors without flattening them:
// the refusal of one group and the sentinel of the other both stay
// reachable, and the "; "-joined, sorted, host-attributed text is unchanged.
func TestPushFleetRunKeepsPerGroupErrorChains(t *testing.T) {
	setupFanoutErrors(t, false)
	err := PushFleetRun(context.Background(), "fe", "", 0, remote.DefaultHostTimeout, "fanout_err")
	want := `fleet "fe": cluster "ca": refuse1: chunk 0 (elevate=false): remote apply: ` +
		fanoutDangling.Error() + `; cluster "cb": boom1: chunk 0 (elevate=false): boom`
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v\nwant %q", err, want)
	}
	assertRefusalChain(t, err)
	if !errors.Is(err, errFanoutBoom) {
		t.Fatalf("errors.Is(sentinel) failed through PushFleetRun: %v", err)
	}
}

// A canceled fleet push still reports each group's abort (no host blamed),
// and context.Canceled stays reachable through the fleet aggregate.
func TestPushFleetRunCanceledKeepsContextChain(t *testing.T) {
	setupFanoutErrors(t, true)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := PushFleetRun(ctx, "fe", "", 0, remote.DefaultHostTimeout, "fanout_err")
	want := `fleet "fe": cluster "ca": aborted: chunk 0 (elevate=false): context canceled; ` +
		`cluster "cb": aborted: chunk 0 (elevate=false): context canceled`
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v\nwant %q", err, want)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("errors.Is(Canceled) failed through PushFleetRun: %v", err)
	}
}

// A fleet host timeout keeps its wording and the DeadlineExceeded cause.
func TestPushFleetRunHostTimeoutKeepsDeadlineChain(t *testing.T) {
	setupFanoutErrors(t, true)
	err := PushFleetRun(context.Background(), "fe", "", 0, 20*time.Millisecond, "fanout_err")
	if err == nil || !strings.Contains(err.Error(), "(host timeout after 20ms)") {
		t.Fatalf("err = %v, want a host timeout report", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("errors.Is(DeadlineExceeded) failed through PushFleetRun: %v", err)
	}
}
