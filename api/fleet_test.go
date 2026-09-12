package api

import (
	"bytes"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/snonux/gonf/resource"
)

func TestHostFleetRegistry(t *testing.T) {
	ResetInventory()
	h1 := Host("a", WithSSHUser("u"), WithSSHHost("a.example"), WithSSHPort(22))
	h2 := Host("b", WithSSHHost("b.example"))
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
	if len(infos) != 2 || infos[0].Name != "a" || infos[0].User != "u" {
		t.Fatalf("Hosts=%#v", infos)
	}
	finfos := Fleets()
	if len(finfos) != 1 || finfos[0].Parallelism != 2 || len(finfos[0].Hosts) != 2 {
		t.Fatalf("Fleets=%#v", finfos)
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

func TestPushFleetParallel(t *testing.T) {
	ResetInventory()
	ResetTasks()
	resource.ResetRepository()
	Task("fleet_demo", "", func() {})

	h1 := Host("h1", WithSSHUser("rex"), WithSSHHost("h1.example"), WithSSHPort(2))
	h2 := Host("h2", WithSSHUser("rex"), WithSSHHost("h2.example"), WithSSHPort(2))
	Fleet("frontends", h1, h2).Parallel(2)

	old := sshRunner
	t.Cleanup(func() { sshRunner = old })

	var inFlight, maxFlight atomic.Int32
	var saw int32
	sshRunner = func(stdin io.Reader, argv []string) error {
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
		if len(argv) < 5 || argv[1] != "-p" || argv[2] != "2" {
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

	old := sshRunner
	t.Cleanup(func() { sshRunner = old })

	var inFlight, maxFlight atomic.Int32
	sshRunner = func(stdin io.Reader, argv []string) error {
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

	old := sshRunner
	t.Cleanup(func() { sshRunner = old })
	sshRunner = func(stdin io.Reader, argv []string) error {
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
	old := sshRunner
	t.Cleanup(func() { sshRunner = old })
	var stdin []byte
	var argv []string
	sshRunner = func(r io.Reader, a []string) error {
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

func TestPushFleetDryRun(t *testing.T) {
	ResetInventory()
	ResetTasks()
	resource.ResetRepository()
	resource.SetDryRun(true)
	t.Cleanup(func() { resource.SetDryRun(false) })
	Task("dry", "", func() {})

	Fleet("dryf", Host("d1", WithSSHHost("d1.example")))

	old := sshRunner
	t.Cleanup(func() { sshRunner = old })
	var remote string
	sshRunner = func(stdin io.Reader, argv []string) error {
		remote = argv[len(argv)-1]
		_, _ = io.Copy(io.Discard, stdin)
		return nil
	}
	if err := PushFleet("dryf", "dry"); err != nil {
		t.Fatal(err)
	}
	if remote != "gonf apply -n -" {
		t.Fatalf("remote=%q", remote)
	}
}
