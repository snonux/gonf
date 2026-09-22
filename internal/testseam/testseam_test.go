package testseam

import (
	"testing"

	"github.com/snonux/gonf/internal/exec"
)

// fakeCleaner collects cleanups so a test decides when they run, the way
// testing runs them: last registered first.
type fakeCleaner struct{ fns []func() }

func (c *fakeCleaner) Cleanup(fn func()) { c.fns = append(c.fns, fn) }

func (c *fakeCleaner) run() {
	for i := len(c.fns) - 1; i >= 0; i-- {
		c.fns[i]()
	}
	c.fns = nil
}

// named returns a Run fake whose stdout identifies it.
func named(name string) Run {
	return func(string, ...string) (string, string, int, error) { return name, "", 0, nil }
}

// stdout calls run and returns its stdout, or "real" for a nil runner.
func stdout(run Run) string {
	if run == nil {
		return "real"
	}
	out, _, _, _ := run("x")
	return out
}

// TestFakeMergesAndRestoresInOrder pins the slot contract every Fake* call
// relies on: a nil field keeps the runner in effect, and cleanups unwind
// nested fakes back to the previous value and finally to none.
func TestFakeMergesAndRestoresInOrder(t *testing.T) {
	var outer, inner fakeCleaner
	FakeCommand(&outer, Command{Probe: named("probe-1")})
	FakeCommand(&inner, Command{Run: func(exec.Opts, string, ...string) (string, string, int, error) {
		return "run-2", "", 0, nil
	}})
	if got := stdout(CommandFakes().Probe); got != "probe-1" {
		t.Fatalf("probe after a Run-only fake = %q, want the kept probe-1", got)
	}
	if CommandFakes().Run == nil {
		t.Fatal("Run fake not installed")
	}
	inner.run()
	if CommandFakes().Run != nil || stdout(CommandFakes().Probe) != "probe-1" {
		t.Fatal("inner cleanup did not restore the outer fake")
	}
	outer.run()
	if f := CommandFakes(); f.Run != nil || f.Probe != nil {
		t.Fatal("outer cleanup did not restore the real runners")
	}
}

// TestSingleSlotFakes covers the whole-value slots: each accessor returns the
// installed fake until cleanup and nil afterwards.
func TestSingleSlotFakes(t *testing.T) {
	detect := func() (string, error) { return "dnf", nil }
	tests := []struct {
		name    string
		install func(Cleaner)
		active  func() bool
	}{
		{"systemctl", func(c Cleaner) { FakeSystemctl(c, named("ctl")) }, func() bool { return stdout(Systemctl()) == "ctl" }},
		{"service runner", func(c Cleaner) { FakeServiceRunner(c, named("svc")) }, func() bool {
			return stdout(ServiceRunner()) == "svc" && stdout(Systemctl()) == "svc"
		}},
		{"package manager", func(c Cleaner) { FakePackageManager(c, detect) }, func() bool { return PackageManager() != nil }},
		{"service manager", func(c Cleaner) { FakeServiceManager(c, detect) }, func() bool { return ServiceManager() != nil }},
		{"package runner", func(c Cleaner) { FakePackageRunner(c, Package{Run: named("pkg")}) }, func() bool {
			return stdout(PackageFakes().Run) == "pkg" && PackageFakes().RunEnv == nil
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var c fakeCleaner
			tt.install(&c)
			if !tt.active() {
				t.Fatal("fake not in effect after install")
			}
			c.run()
			if tt.active() {
				t.Fatal("fake still in effect after cleanup")
			}
		})
	}
}

// TestFakeCrontabLockFlag: any crontab fake reports faked (so resource/cron
// takes its in-process lock) unless the latest call asked for the real lock.
func TestFakeCrontabLockFlag(t *testing.T) {
	var c fakeCleaner
	if _, faked := CrontabFakes(); faked {
		t.Fatal("crontab reported faked before any fake")
	}
	FakeCrontab(&c, Crontab{Read: named("tab")})
	f, faked := CrontabFakes()
	if !faked || f.RealLock || stdout(f.Read) != "tab" || f.Write != nil {
		t.Fatalf("after Read fake: %+v faked=%t", f, faked)
	}
	FakeCrontab(&c, Crontab{RealLock: true})
	if f, _ := CrontabFakes(); !f.RealLock || stdout(f.Read) != "tab" {
		t.Fatalf("RealLock fake lost the kept Read or the flag: %+v", f)
	}
	c.run()
	if _, faked := CrontabFakes(); faked {
		t.Fatal("crontab still faked after cleanup")
	}
}
