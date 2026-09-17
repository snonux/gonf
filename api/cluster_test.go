package api

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/snonux/gonf/internal/orchestrate"
	"github.com/snonux/gonf/internal/remote"
	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
)

func TestHostClusterRegistry(t *testing.T) {
	ResetInventory()
	h1 := Host("a",
		WithSSHUser("u"),
		WithSSHHost("a.example"),
		WithSSHPort(22),
		WithPrivilege(PrivilegeDoas),
	)
	h2 := Host("b",
		WithSSHHost("b.example"),
		WithSSHPort(22),
		WithPrivilege(PrivilegeSudo),
	)
	Cluster("grp", h1, h2).Parallel(2)

	got, ok := LookupHost("a")
	if !ok || got.name != "a" {
		t.Fatalf("LookupHost: %#v %v", got, ok)
	}
	if MustHost("a").name != "a" {
		t.Fatal("MustHost")
	}
	fg, ok := LookupCluster("grp")
	if !ok || fg.name != "grp" {
		t.Fatalf("LookupCluster: %#v %v", fg, ok)
	}
	infos := Hosts()
	if len(infos) != 2 {
		t.Fatalf("Hosts=%#v", infos)
	}
	byName := map[string]HostInfo{}
	for _, h := range infos {
		byName[h.Name] = h
	}
	a := byName["a"]
	if a.User != "u" || a.SSHHost != "a.example" || a.Port != 22 || a.Privilege != "doas" {
		t.Fatalf("host a = %+v, want user/u host/a.example port/22 privilege/doas", a)
	}
	b := byName["b"]
	if b.SSHHost != "b.example" || b.Port != 22 || b.Privilege != "sudo" {
		t.Fatalf("host b = %+v, want host/b.example port/22 privilege/sudo", b)
	}
	// Port 0 must stay distinguishable from an explicit port: ssh omits -p
	// when Port is 0, so a missing WithSSHPort is a silent ~/.ssh/config
	// footgun — Hosts() must surface the zero so callers can catch it.
	zero := Host("z", WithSSHHost("z.example"))
	_ = zero
	for _, h := range Hosts() {
		if h.Name == "z" && h.Port != 0 {
			t.Fatalf("host z Port = %d, want 0 when WithSSHPort omitted", h.Port)
		}
	}
	finfos := Clusters()
	if len(finfos) != 1 || finfos[0].Parallelism != 2 || len(finfos[0].Hosts) != 2 {
		t.Fatalf("Fleets=%#v", finfos)
	}
	gotNames := MustCluster("grp").HostNames()
	if len(gotNames) != 2 || gotNames[0] != "a" || gotNames[1] != "b" {
		t.Fatalf("HostNames = %v, want [a b] in registration order", gotNames)
	}
}

func TestHostValueAndClusterHosts(t *testing.T) {
	ResetInventory()
	ResetTasks()
	t.Cleanup(func() {
		ResetInventory()
		ResetTasks()
	})

	h1 := Host("a",
		WithSSHHost("a.example"),
		WithValue("cron", [2]string{"6", "7"}),
	)
	h2 := Host("b", WithSSHHost("b.example"))
	h2.SetValue("cron", [2]string{"22", "23"})
	Cluster("grp", h1, h2)

	if got := MustHostValue[[2]string]("a", "cron"); got != [2]string{"6", "7"} {
		t.Fatalf("host a cron = %v", got)
	}
	if got := MustHostValue[[2]string]("b", "cron"); got != [2]string{"22", "23"} {
		t.Fatalf("host b cron = %v", got)
	}

	var seen []string
	Task("demo_body", "", func() {
		seen = append(seen, ClusterHosts()...)
		_ = MustHostValue[[2]string](ClusterHosts()[0], "cron")
	}, WithTaskCluster("grp"))

	if _, err := RecordPlan("fleet-hosts", t.TempDir(), "demo_body"); err != nil {
		t.Fatalf("RecordPlan: %v", err)
	}
	if len(seen) != 2 || seen[0] != "a" || seen[1] != "b" {
		t.Fatalf("ClusterHosts during task = %v, want [a b]", seen)
	}
}

func TestFleetOfClusters(t *testing.T) {
	ResetInventory()
	h1 := Host("a", WithSSHHost("a.example"))
	h2 := Host("b", WithSSHHost("b.example"))
	h3 := Host("c", WithSSHHost("c.example"))
	c1 := Cluster("edge", h1, h2)
	c2 := Cluster("core", h2, h3) // overlap on b
	Fleet("homelab", c1, c2)

	got := MustFleet("homelab").HostNames()
	want := []string{"a", "b", "c"}
	if len(got) != len(want) {
		t.Fatalf("HostNames=%v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("HostNames=%v, want %v", got, want)
		}
	}
	cinfo := Fleets()
	if len(cinfo) != 1 || cinfo[0].Name != "homelab" {
		t.Fatalf("Fleets=%#v", cinfo)
	}
}

func TestClusterDuplicateHostNames(t *testing.T) {
	ResetInventory()
	h := Host("dup", WithSSHHost("dup.example"))
	err := checkClusterHostsUnique([]HostRef{h, {name: "dup"}})
	if err == nil {
		t.Fatal("expected duplicate error")
	}
}

// hasPortPair reports whether argv contains an adjacent "-p port" pair.
func hasPortPair(argv []string, port string) bool {
	for i, a := range argv {
		if a == "-p" && i+1 < len(argv) && argv[i+1] == port {
			return true
		}
	}
	return false
}

func TestPushClusterParallel(t *testing.T) {
	ResetInventory()
	ResetTasks()
	resource.ResetRepository()
	Task("fleet_demo", "", func() {})

	h1 := Host("h1", WithSSHUser("rex"), WithSSHHost("h1.example"), WithSSHPort(2))
	h2 := Host("h2", WithSSHUser("rex"), WithSSHHost("h2.example"), WithSSHPort(2))
	Cluster("frontends", h1, h2).Parallel(2)

	old := remote.SSHRunner
	restoreProbe := remote.AssumeRemotePlanCurrent()
	t.Cleanup(func() {
		remote.SSHRunner = old
		restoreProbe()
	})

	var inFlight, maxFlight atomic.Int32
	var saw int32
	remote.SSHRunner = func(ctx context.Context, stdin io.Reader, argv []string) error {
		n := inFlight.Add(1)
		for {
			cur := maxFlight.Load()
			if n <= cur || maxFlight.CompareAndSwap(cur, n) {
				break
			}
		}
		defer inFlight.Add(-1)
		time.Sleep(20 * time.Millisecond)
		atomic.AddInt32(&saw, 1)
		_, _ = io.Copy(io.Discard, stdin)
		// Position-independent: the ssh argv carries the -p 2 pair (the
		// generated ConnectTimeout option sits before it).
		if len(argv) < 5 || !hasPortPair(argv, "2") {
			t.Errorf("argv=%v", argv)
		}
		return nil
	}

	if err := PushCluster("frontends", "fleet_demo"); err != nil {
		t.Fatal(err)
	}
	if saw != 2 {
		t.Fatalf("saw=%d", saw)
	}
}

func TestPushClusterSerialLimit(t *testing.T) {
	ResetInventory()
	ResetTasks()
	resource.ResetRepository()
	Task("fleet_serial", "", func() {})

	Cluster("serial",
		Host("s1", WithSSHHost("s1.example")),
		Host("s2", WithSSHHost("s2.example")),
	).Parallel(1)

	old := remote.SSHRunner
	restoreProbe := remote.AssumeRemotePlanCurrent()
	t.Cleanup(func() {
		remote.SSHRunner = old
		restoreProbe()
	})

	var inFlight, maxFlight atomic.Int32
	remote.SSHRunner = func(ctx context.Context, stdin io.Reader, argv []string) error {
		n := inFlight.Add(1)
		for {
			cur := maxFlight.Load()
			if n <= cur || maxFlight.CompareAndSwap(cur, n) {
				break
			}
		}
		defer inFlight.Add(-1)
		time.Sleep(30 * time.Millisecond)
		_, _ = io.Copy(io.Discard, stdin)
		return nil
	}

	if err := PushCluster("serial", "fleet_serial"); err != nil {
		t.Fatal(err)
	}
	if maxFlight.Load() != 1 {
		t.Fatalf("maxFlight=%d want 1", maxFlight.Load())
	}
}

func TestPushClusterAggregatesErrors(t *testing.T) {
	ResetInventory()
	ResetTasks()
	resource.ResetRepository()
	Task("fleet_err", "", func() {})

	Cluster("errs",
		Host("e1", WithSSHHost("e1.example")),
		Host("e2", WithSSHHost("e2.example")),
	).Parallel(2)

	old := remote.SSHRunner
	restoreProbe := remote.AssumeRemotePlanCurrent()
	t.Cleanup(func() {
		remote.SSHRunner = old
		restoreProbe()
	})
	remote.SSHRunner = func(ctx context.Context, stdin io.Reader, argv []string) error {
		_, _ = io.Copy(io.Discard, stdin)
		return io.ErrUnexpectedEOF
	}

	err := PushCluster("errs", "fleet_err")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "e1") || !strings.Contains(err.Error(), "e2") {
		t.Fatalf("err=%v", err)
	}
}

func TestPushHostAndPayloadMagic(t *testing.T) {
	ResetInventory()
	ResetTasks()
	resource.ResetRepository()
	Task("host_push", "", func() {})

	Host("solo", WithSSHUser("paul"), WithSSHHost("solo.example"), WithSSHIdentity("/tmp/id"))
	old := remote.SSHRunner
	restoreProbe := remote.AssumeRemotePlanCurrent()
	t.Cleanup(func() {
		remote.SSHRunner = old
		restoreProbe()
	})
	var stdin []byte
	var argv []string
	remote.SSHRunner = func(ctx context.Context, r io.Reader, a []string) error {
		argv = append([]string(nil), a...)
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		stdin = buf.Bytes()
		return nil
	}

	if err := PushHost(MustHost("solo"), "host_push"); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(stdin, []byte("GONF-PUSH/1")) {
		t.Fatalf("missing magic")
	}
	joined := strings.Join(argv, " ")
	if !strings.Contains(joined, "paul@solo.example") || !strings.Contains(joined, "-i") {
		t.Fatalf("argv=%v", argv)
	}
}

// A failing host must cancel its in-flight siblings: e1 fails immediately,
// e2 blocks until its per-host context is canceled by the errgroup. e2 is
// then reported as aborted, not as an independent host failure.
func TestPushClusterCancelsInFlightOnFailure(t *testing.T) {
	ResetInventory()
	ResetTasks()
	resource.ResetRepository()
	Task("fleet_cancel", "", func() {})

	Cluster("cancels",
		Host("e1", WithSSHHost("e1.example")),
		Host("e2", WithSSHHost("e2.example")),
	).Parallel(2)

	old := remote.SSHRunner
	restoreProbe := remote.AssumeRemotePlanCurrent()
	t.Cleanup(func() {
		remote.SSHRunner = old
		restoreProbe()
	})
	var e2Canceled atomic.Bool
	remote.SSHRunner = func(ctx context.Context, stdin io.Reader, argv []string) error {
		_, _ = io.Copy(io.Discard, stdin)
		if strings.Contains(argv[len(argv)-2], "e1.example") {
			return errors.New("boom")
		}
		// e2 blocks until the cluster abort kills it.
		<-ctx.Done()
		e2Canceled.Store(true)
		return ctx.Err()
	}

	err := PushCluster("cancels", "fleet_cancel")
	if err == nil {
		t.Fatal("expected the cluster to fail")
	}
	if !strings.Contains(err.Error(), "e1") || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err=%v, want e1's failure", err)
	}
	if strings.Contains(err.Error(), "e2") {
		t.Fatalf("canceled sibling must not be reported as failed: %v", err)
	}
	if !e2Canceled.Load() {
		t.Fatal("e2's ssh was not canceled by e1's failure")
	}
}

// A host stuck in ssh must not hold its errgroup slot forever: the per-host
// timeout kills the push to that host and reports it as a fleet failure.
func TestPushClusterHostTimeout(t *testing.T) {
	ResetInventory()
	ResetTasks()
	resource.ResetRepository()
	Task("fleet_slow", "", func() {})

	Cluster("slowf", Host("s1", WithSSHHost("s1.example")))

	old := remote.SSHRunner
	restoreProbe := remote.AssumeRemotePlanCurrent()
	t.Cleanup(func() {
		remote.SSHRunner = old
		restoreProbe()
	})
	remote.SSHRunner = func(ctx context.Context, stdin io.Reader, argv []string) error {
		_, _ = io.Copy(io.Discard, stdin)
		<-ctx.Done()
		return ctx.Err()
	}

	err := PushClusterRun(context.Background(), "slowf", "", 1, 50*time.Millisecond, "fleet_slow")
	if err == nil {
		t.Fatal("expected the host timeout to fail the push")
	}
	if !strings.Contains(err.Error(), "host timeout") {
		t.Fatalf("err=%v, want a host timeout report", err)
	}
}

// A SIGINT-style cancellation (CLI context) aborts the whole fan-out: no
// host is blamed and the cluster reports an abort instead of pretending a
// partial run succeeded.
func TestPushClusterAbortsOnCanceledContext(t *testing.T) {
	ResetInventory()
	ResetTasks()
	resource.ResetRepository()
	Task("fleet_abort", "", func() {})

	Cluster("abortf", Host("a1", WithSSHHost("a1.example")))

	old := remote.SSHRunner
	restoreProbe := remote.AssumeRemotePlanCurrent()
	t.Cleanup(func() {
		remote.SSHRunner = old
		restoreProbe()
	})
	remote.SSHRunner = func(ctx context.Context, stdin io.Reader, argv []string) error {
		_, _ = io.Copy(io.Discard, stdin)
		<-ctx.Done()
		return ctx.Err()
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := PushClusterRun(ctx, "abortf", "", 1, remote.DefaultHostTimeout, "fleet_abort")
	if err == nil {
		t.Fatal("expected an abort error")
	}
	if !strings.Contains(err.Error(), "aborted") {
		t.Fatalf("err=%v, want an abort report", err)
	}
	if strings.Contains(err.Error(), "a1:") {
		t.Fatalf("abort must not blame the killed host: %v", err)
	}
}

func TestPushClusterDryRun(t *testing.T) {
	ResetInventory()
	ResetTasks()
	resource.ResetRepository()
	resource.SetDryRun(true)
	t.Cleanup(func() { resource.SetDryRun(false) })
	Task("dry", "", func() {})

	Cluster("dryf", Host("d1", WithSSHHost("d1.example")))

	old := remote.SSHRunner
	restoreProbe := remote.AssumeRemotePlanCurrent()
	t.Cleanup(func() {
		remote.SSHRunner = old
		restoreProbe()
	})
	var sawRemote string
	remote.SSHRunner = func(ctx context.Context, stdin io.Reader, argv []string) error {
		sawRemote = argv[len(argv)-1]
		_, _ = io.Copy(io.Discard, stdin)
		return nil
	}
	if err := PushCluster("dryf", "dry"); err != nil {
		t.Fatal(err)
	}
	if sawRemote != "gonf apply -n -" {
		t.Fatalf("remote=%q", sawRemote)
	}
}

// TestPushHostsSharedByClusterAndFleet confirms PushClusterRun and
// PushFleetRun bottom out in the exact same orchestrate.Push helper: calling
// it directly, once per "path", must push to every host exactly once either
// way. This is the (a) requirement from task n5 — proving the two entry
// points share one push pipeline instead of each carrying its own copy of
// the targets/labels/Fanout loop.
func TestPushHostsSharedByClusterAndFleet(t *testing.T) {
	ResetInventory()
	ResetTasks()
	resource.ResetRepository()
	Task("shared_push", "", func() {})

	h1 := Host("sh1", WithSSHHost("sh1.example"))
	h2 := Host("sh2", WithSSHHost("sh2.example"))

	old := remote.SSHRunner
	restoreProbe := remote.AssumeRemotePlanCurrent()
	t.Cleanup(func() {
		remote.SSHRunner = old
		restoreProbe()
	})
	var calls atomic.Int32
	remote.SSHRunner = func(ctx context.Context, stdin io.Reader, argv []string) error {
		calls.Add(1)
		_, _ = io.Copy(io.Discard, stdin)
		return nil
	}

	mem := plan.NewMemoryStore()
	ops, err := RecordPlanTo("shared-test", mem, "shared_push")
	if err != nil {
		t.Fatal(err)
	}

	hostNames := []string{h1.Name(), h2.Name()}

	// "cluster-shaped" call.
	if err := orchestrate.Push(context.Background(), "cluster-label", "shared-test", hostNames, 2, remote.DefaultHostTimeout, ops, mem); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatalf("cluster-shaped orchestrate.Push calls=%d, want 2", calls.Load())
	}

	// "fleet-group-shaped" call: same helper, same hosts, different label —
	// exactly how PushFleetRun invokes it once per member cluster group.
	calls.Store(0)
	if err := orchestrate.Push(context.Background(), "fleet-group-label", "shared-test", hostNames, 2, remote.DefaultHostTimeout, ops, mem); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatalf("fleet-group-shaped orchestrate.Push calls=%d, want 2", calls.Load())
	}
}

// TestPushFleetHonorsClusterParallel is the regression test for the fleet
// parallelism bug: a cluster with a distinctive Parallel(n) (here 1, fully
// serial) must still be pushed at that concurrency when reached through a
// fleet, not at defaultClusterParallelism (5). Before the n5 fix,
// PushFleetRun always used defaultClusterParallelism for the whole fleet
// fan-out regardless of each member cluster's own Parallel(n) setting; with
// 3 hosts and a default limit of 5, that bug would let all 3 hosts push
// concurrently (maxFlight would land at 3, not 1). Reverting the
// PushFleetRun fix (restoring the flat defaultClusterParallelism fan-out)
// makes this test fail, confirming it actually catches the original bug.
func TestPushFleetHonorsClusterParallel(t *testing.T) {
	ResetInventory()
	ResetTasks()
	resource.ResetRepository()
	Task("fleet_serial_via_fleet", "", func() {})

	Cluster("fragile",
		Host("fr1", WithSSHHost("fr1.example")),
		Host("fr2", WithSSHHost("fr2.example")),
		Host("fr3", WithSSHHost("fr3.example")),
	).Parallel(1) // fragile/rate-limited hosts: one push at a time, by design.
	Fleet("homelab", MustCluster("fragile"))

	old := remote.SSHRunner
	restoreProbe := remote.AssumeRemotePlanCurrent()
	t.Cleanup(func() {
		remote.SSHRunner = old
		restoreProbe()
	})

	var inFlight, maxFlight atomic.Int32
	remote.SSHRunner = func(ctx context.Context, stdin io.Reader, argv []string) error {
		n := inFlight.Add(1)
		for {
			cur := maxFlight.Load()
			if n <= cur || maxFlight.CompareAndSwap(cur, n) {
				break
			}
		}
		defer inFlight.Add(-1)
		time.Sleep(30 * time.Millisecond)
		_, _ = io.Copy(io.Discard, stdin)
		return nil
	}

	if err := PushFleet("homelab", "fleet_serial_via_fleet"); err != nil {
		t.Fatal(err)
	}
	if maxFlight.Load() != 1 {
		t.Fatalf("maxFlight=%d, want 1: fleet push must honor cluster %q's own Parallel(1), not defaultClusterParallelism", maxFlight.Load(), "fragile")
	}
}

// TestPushFleetFailureCancelsOtherClusters is the regression test for the n5
// follow-up fix: giving each member cluster its own remote.Fanout call (so
// its own Parallel(n) governs its own concurrency independently) must not
// narrow the pre-existing whole-fleet fail-fast contract documented in
// docs/plan.md "Timeouts and cancellation" ("a failing host cancels its
// in-flight siblings ... the fleet error reports the abort reason once").
// Cluster "bad" (Parallel(1)) and cluster "good" (Parallel(2), a distinct
// value, so this also re-confirms each group still gets its OWN concurrency
// ceiling) are pushed together via one fleet. "bad"'s only host fails after
// a short delay, once "good"'s two hosts are already in flight (blocked on
// ctx.Done()). Both of "good"'s hosts must be canceled — not left to run to
// completion — even though the failure happened in a different member
// cluster's group. Before the fix (independent, uncoupled contexts per
// group), "good"'s hosts would run to completion instead.
func TestPushFleetFailureCancelsOtherClusters(t *testing.T) {
	ResetInventory()
	ResetTasks()
	resource.ResetRepository()
	Task("fleet_cross_cancel", "", func() {})

	Cluster("bad", Host("bad1", WithSSHHost("bad1.example"))).Parallel(1)
	Cluster("good",
		Host("g1", WithSSHHost("g1.example")),
		Host("g2", WithSSHHost("g2.example")),
	).Parallel(2) // distinct from "bad"'s Parallel(1): both g1 and g2 run at once.
	Fleet("mixed", MustCluster("bad"), MustCluster("good"))

	old := remote.SSHRunner
	restoreProbe := remote.AssumeRemotePlanCurrent()
	t.Cleanup(func() {
		remote.SSHRunner = old
		restoreProbe()
	})

	var g1Canceled, g2Canceled, g1Completed, g2Completed atomic.Bool
	blockUntilCanceledOrTimeout := func(ctx context.Context, canceled, completed *atomic.Bool) error {
		select {
		case <-ctx.Done():
			canceled.Store(true)
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
			completed.Store(true)
			return nil
		}
	}
	remote.SSHRunner = func(ctx context.Context, stdin io.Reader, argv []string) error {
		_, _ = io.Copy(io.Discard, stdin)
		dest := argv[len(argv)-2]
		switch {
		case strings.Contains(dest, "bad1.example"):
			// Give "good"'s hosts time to start and block on ctx.Done()
			// before this cluster's host fails.
			time.Sleep(30 * time.Millisecond)
			return errors.New("boom")
		case strings.Contains(dest, "g1.example"):
			return blockUntilCanceledOrTimeout(ctx, &g1Canceled, &g1Completed)
		default: // g2.example
			return blockUntilCanceledOrTimeout(ctx, &g2Canceled, &g2Completed)
		}
	}

	err := PushFleet("mixed", "fleet_cross_cancel")
	if err == nil {
		t.Fatal("expected the fleet push to fail")
	}
	if !strings.Contains(err.Error(), "bad1") || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err=%v, want bad1's failure reported", err)
	}
	if !strings.Contains(err.Error(), "aborted") {
		t.Fatalf("err=%v, want good cluster's abort reported", err)
	}
	if g1Completed.Load() || g2Completed.Load() {
		t.Fatalf("good cluster's hosts ran to completion instead of being canceled by bad cluster's failure (g1Completed=%v g2Completed=%v)",
			g1Completed.Load(), g2Completed.Load())
	}
	if !g1Canceled.Load() || !g2Canceled.Load() {
		t.Fatalf("good cluster's in-flight hosts were not canceled by bad cluster's failure (g1Canceled=%v g2Canceled=%v)",
			g1Canceled.Load(), g2Canceled.Load())
	}
}

// TestPushFleetParallelOverrideAppliesToAllGroups confirms `-j`
// (parallelOverride) uniformly overrides EVERY member cluster's group limit,
// not just one — winning over both a cluster explicitly throttled below the
// override ("one", Parallel(1)) and one explicitly opened up above it
// ("five", Parallel(5)). Coverage for parallelOverride across 2+ fleet
// groups was previously code-traced only (PushFleetRun's `if
// parallelOverride > 0 { limit = parallelOverride }` inside the per-group
// loop) with no direct regression test.
func TestPushFleetParallelOverrideAppliesToAllGroups(t *testing.T) {
	ResetInventory()
	ResetTasks()
	resource.ResetRepository()
	Task("fleet_override", "", func() {})

	Cluster("one",
		Host("o1", WithSSHHost("o1.example")),
		Host("o2", WithSSHHost("o2.example")),
		Host("o3", WithSSHHost("o3.example")),
	).Parallel(1)
	Cluster("five",
		Host("f1", WithSSHHost("f1.example")),
		Host("f2", WithSSHHost("f2.example")),
		Host("f3", WithSSHHost("f3.example")),
	).Parallel(5)
	Fleet("over", MustCluster("one"), MustCluster("five"))

	old := remote.SSHRunner
	restoreProbe := remote.AssumeRemotePlanCurrent()
	t.Cleanup(func() {
		remote.SSHRunner = old
		restoreProbe()
	})

	var oneInFlight, oneMaxFlight, fiveInFlight, fiveMaxFlight atomic.Int32
	track := func(dest string, inFlight, maxFlight *atomic.Int32) {
		n := inFlight.Add(1)
		for {
			cur := maxFlight.Load()
			if n <= cur || maxFlight.CompareAndSwap(cur, n) {
				break
			}
		}
		defer inFlight.Add(-1)
		time.Sleep(30 * time.Millisecond)
		_ = dest
	}
	remote.SSHRunner = func(ctx context.Context, stdin io.Reader, argv []string) error {
		_, _ = io.Copy(io.Discard, stdin)
		dest := argv[len(argv)-2]
		if strings.HasPrefix(dest, "o") {
			track(dest, &oneInFlight, &oneMaxFlight)
		} else {
			track(dest, &fiveInFlight, &fiveMaxFlight)
		}
		return nil
	}

	if err := PushFleetRun(context.Background(), "over", "", 2, remote.DefaultHostTimeout, "fleet_override"); err != nil {
		t.Fatal(err)
	}
	if oneMaxFlight.Load() != 2 {
		t.Fatalf("cluster %q maxFlight=%d, want 2: -j must override its Parallel(1)", "one", oneMaxFlight.Load())
	}
	if fiveMaxFlight.Load() != 2 {
		t.Fatalf("cluster %q maxFlight=%d, want 2: -j must override its Parallel(5)", "five", fiveMaxFlight.Load())
	}
}
