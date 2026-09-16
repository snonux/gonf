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

	"github.com/snonux/gonf/internal/remote"
	"github.com/snonux/gonf/resource"
)

func TestHostFleetRegistry(t *testing.T) {
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
	Fleet("grp", h1, h2).Parallel(2)

	got, ok := LookupHost("a")
	if !ok || got.name != "a" {
		t.Fatalf("LookupHost: %#v %v", got, ok)
	}
	if MustHost("a").name != "a" {
		t.Fatal("MustHost")
	}
	fg, ok := LookupFleet("grp")
	if !ok || fg.name != "grp" {
		t.Fatalf("LookupFleet: %#v %v", fg, ok)
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
	finfos := Fleets()
	if len(finfos) != 1 || finfos[0].Parallelism != 2 || len(finfos[0].Hosts) != 2 {
		t.Fatalf("Fleets=%#v", finfos)
	}
	gotNames := MustFleet("grp").HostNames()
	if len(gotNames) != 2 || gotNames[0] != "a" || gotNames[1] != "b" {
		t.Fatalf("HostNames = %v, want [a b] in registration order", gotNames)
	}
}

func TestMustMapValue(t *testing.T) {
	m := map[string]string{"a": "1"}
	if got := MustMapValue(m, "a", "cron window"); got != "1" {
		t.Fatalf("got %q", got)
	}
}

func TestFleetDuplicateHostNames(t *testing.T) {
	ResetInventory()
	h := Host("dup", WithSSHHost("dup.example"))
	err := checkFleetHostsUnique([]HostRef{h, {name: "dup"}})
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

func TestPushFleetParallel(t *testing.T) {
	ResetInventory()
	ResetTasks()
	resource.ResetRepository()
	Task("fleet_demo", "", func() {})

	h1 := Host("h1", WithSSHUser("rex"), WithSSHHost("h1.example"), WithSSHPort(2))
	h2 := Host("h2", WithSSHUser("rex"), WithSSHHost("h2.example"), WithSSHPort(2))
	Fleet("frontends", h1, h2).Parallel(2)

	old := remote.SSHRunner
	t.Cleanup(func() { remote.SSHRunner = old })

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

	if err := PushFleet("frontends", "fleet_demo"); err != nil {
		t.Fatal(err)
	}
	if saw != 2 {
		t.Fatalf("saw=%d", saw)
	}
}

func TestPushFleetSerialLimit(t *testing.T) {
	ResetInventory()
	ResetTasks()
	resource.ResetRepository()
	Task("fleet_serial", "", func() {})

	Fleet("serial",
		Host("s1", WithSSHHost("s1.example")),
		Host("s2", WithSSHHost("s2.example")),
	).Parallel(1)

	old := remote.SSHRunner
	t.Cleanup(func() { remote.SSHRunner = old })

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

	if err := PushFleet("serial", "fleet_serial"); err != nil {
		t.Fatal(err)
	}
	if maxFlight.Load() != 1 {
		t.Fatalf("maxFlight=%d want 1", maxFlight.Load())
	}
}

func TestPushFleetAggregatesErrors(t *testing.T) {
	ResetInventory()
	ResetTasks()
	resource.ResetRepository()
	Task("fleet_err", "", func() {})

	Fleet("errs",
		Host("e1", WithSSHHost("e1.example")),
		Host("e2", WithSSHHost("e2.example")),
	).Parallel(2)

	old := remote.SSHRunner
	t.Cleanup(func() { remote.SSHRunner = old })
	remote.SSHRunner = func(ctx context.Context, stdin io.Reader, argv []string) error {
		_, _ = io.Copy(io.Discard, stdin)
		return io.ErrUnexpectedEOF
	}

	err := PushFleet("errs", "fleet_err")
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
	t.Cleanup(func() { remote.SSHRunner = old })
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
func TestPushFleetCancelsInFlightOnFailure(t *testing.T) {
	ResetInventory()
	ResetTasks()
	resource.ResetRepository()
	Task("fleet_cancel", "", func() {})

	Fleet("cancels",
		Host("e1", WithSSHHost("e1.example")),
		Host("e2", WithSSHHost("e2.example")),
	).Parallel(2)

	old := remote.SSHRunner
	t.Cleanup(func() { remote.SSHRunner = old })
	var e2Canceled atomic.Bool
	remote.SSHRunner = func(ctx context.Context, stdin io.Reader, argv []string) error {
		_, _ = io.Copy(io.Discard, stdin)
		if strings.Contains(argv[len(argv)-2], "e1.example") {
			return errors.New("boom")
		}
		// e2 blocks until the fleet abort kills it.
		<-ctx.Done()
		e2Canceled.Store(true)
		return ctx.Err()
	}

	err := PushFleet("cancels", "fleet_cancel")
	if err == nil {
		t.Fatal("expected the fleet to fail")
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
func TestPushFleetHostTimeout(t *testing.T) {
	ResetInventory()
	ResetTasks()
	resource.ResetRepository()
	Task("fleet_slow", "", func() {})

	Fleet("slowf", Host("s1", WithSSHHost("s1.example")))

	old := remote.SSHRunner
	t.Cleanup(func() { remote.SSHRunner = old })
	remote.SSHRunner = func(ctx context.Context, stdin io.Reader, argv []string) error {
		_, _ = io.Copy(io.Discard, stdin)
		<-ctx.Done()
		return ctx.Err()
	}

	err := PushFleetRun(context.Background(), "slowf", "", 1, 50*time.Millisecond, "fleet_slow")
	if err == nil {
		t.Fatal("expected the host timeout to fail the push")
	}
	if !strings.Contains(err.Error(), "host timeout") {
		t.Fatalf("err=%v, want a host timeout report", err)
	}
}

// A SIGINT-style cancellation (CLI context) aborts the whole fan-out: no
// host is blamed and the fleet reports an abort instead of pretending a
// partial run succeeded.
func TestPushFleetAbortsOnCanceledContext(t *testing.T) {
	ResetInventory()
	ResetTasks()
	resource.ResetRepository()
	Task("fleet_abort", "", func() {})

	Fleet("abortf", Host("a1", WithSSHHost("a1.example")))

	old := remote.SSHRunner
	t.Cleanup(func() { remote.SSHRunner = old })
	remote.SSHRunner = func(ctx context.Context, stdin io.Reader, argv []string) error {
		_, _ = io.Copy(io.Discard, stdin)
		<-ctx.Done()
		return ctx.Err()
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := PushFleetRun(ctx, "abortf", "", 1, remote.DefaultHostTimeout, "fleet_abort")
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

func TestPushFleetDryRun(t *testing.T) {
	ResetInventory()
	ResetTasks()
	resource.ResetRepository()
	resource.SetDryRun(true)
	t.Cleanup(func() { resource.SetDryRun(false) })
	Task("dry", "", func() {})

	Fleet("dryf", Host("d1", WithSSHHost("d1.example")))

	old := remote.SSHRunner
	t.Cleanup(func() { remote.SSHRunner = old })
	var sawRemote string
	remote.SSHRunner = func(ctx context.Context, stdin io.Reader, argv []string) error {
		sawRemote = argv[len(argv)-1]
		_, _ = io.Copy(io.Discard, stdin)
		return nil
	}
	if err := PushFleet("dryf", "dry"); err != nil {
		t.Fatal(err)
	}
	if sawRemote != "gonf apply -n -" {
		t.Fatalf("remote=%q", sawRemote)
	}
}
