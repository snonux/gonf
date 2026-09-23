package testseam

import (
	"os"
	osexec "os/exec"
	"strings"
	"testing"
)

// helperEnv makes the test binary run TestParallelFakeHelper for real; the
// parent test (TestFakeRefusesParallelTest) re-executes itself with it set.
const helperEnv = "GONF_TESTSEAM_PARALLEL_HELPER"

// fakeCleaner collects cleanups so a test decides when (and in which order)
// they run, and records Setenv calls.
type fakeCleaner struct {
	fns []func()
	env map[string]string
}

func (c *fakeCleaner) Cleanup(fn func()) { c.fns = append(c.fns, fn) }

func (c *fakeCleaner) Setenv(key, value string) {
	if c.env == nil {
		c.env = map[string]string{}
	}
	c.env[key] = value
}

// run runs the collected cleanups the way testing does: last registered
// first.
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
// relies on: a nil field keeps the runner in effect, each call sets the
// parallel guard, and cleanups unwind nested fakes back to the previous
// value and finally to none.
func TestFakeMergesAndRestoresInOrder(t *testing.T) {
	var outer, inner fakeCleaner
	FakeCrontab(&outer, Crontab{Read: named("read-1")})
	FakeCrontab(&inner, Crontab{Write: func(string, string, ...string) (string, string, int, error) {
		return "write-2", "", 0, nil
	}})
	if outer.env[ParallelGuardEnv] != "1" || inner.env[ParallelGuardEnv] != "1" {
		t.Fatalf("Fake* did not set the parallel guard: %v %v", outer.env, inner.env)
	}
	if got := stdout(CrontabFakes().Read); got != "read-1" {
		t.Fatalf("read after a Write-only fake = %q, want the kept read-1", got)
	}
	if CrontabFakes().Write == nil {
		t.Fatal("Write fake not installed")
	}
	inner.run()
	if CrontabFakes().Write != nil || stdout(CrontabFakes().Read) != "read-1" {
		t.Fatal("inner cleanup did not restore the outer fake")
	}
	outer.run()
	if f := CrontabFakes(); f.Read != nil || f.Write != nil {
		t.Fatal("outer cleanup did not restore the real runners")
	}
}

// TestCleanupOutOfOrderRemovesOnlyItsLayer: a cleanup removes exactly its
// own layer, so the older fake's cleanup running first neither drops the
// newer fake nor lets it survive its own cleanup.
func TestCleanupOutOfOrderRemovesOnlyItsLayer(t *testing.T) {
	var first, second fakeCleaner
	FakeSystemctl(&first, named("first"))
	FakeSystemctl(&second, named("second"))
	first.run()
	if got := stdout(Systemctl()); got != "second" {
		t.Fatalf("after the older cleanup: %q, want the newer fake kept", got)
	}
	second.run()
	if got := stdout(Systemctl()); got != "real" {
		t.Fatalf("after both cleanups: %q, want the real runner", got)
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

// TestCrontabLockChoice: a crontab fake selects the in-process lock unless a
// FakeCrontabLock layer chose the real one, and a later FakeCrontab cannot
// override that choice.
func TestCrontabLockChoice(t *testing.T) {
	var c fakeCleaner
	defer c.run()
	if CrontabInProcessLock() {
		t.Fatal("in-process lock chosen without any fake")
	}
	FakeCrontab(&c, Crontab{Read: named("tab")})
	if f := CrontabFakes(); !CrontabInProcessLock() || stdout(f.Read) != "tab" || f.Write != nil {
		t.Fatalf("after a Read fake: %+v, in-process %t", f, CrontabInProcessLock())
	}
	FakeCrontabLock(&c, false)
	FakeCrontab(&c, Crontab{Write: func(string, string, ...string) (string, string, int, error) { return "", "", 0, nil }})
	if CrontabInProcessLock() {
		t.Fatal("a nested FakeCrontab turned the chosen real lock off")
	}
	if stdout(CrontabFakes().Read) != "tab" {
		t.Fatal("the nested FakeCrontab lost the kept Read fake")
	}
	c.run()
	if CrontabInProcessLock() || CrontabFakes().Read != nil {
		t.Fatal("crontab still faked after cleanup")
	}
}

// TestFakeRefusesParallelTest runs TestParallelFakeHelper in a child test
// binary: a parallel test installing a fake must fail through testing's
// Setenv check instead of racing other tests on the process-global slot.
func TestFakeRefusesParallelTest(t *testing.T) {
	cmd := osexec.Command(os.Args[0], "-test.run=^TestParallelFakeHelper$", "-test.count=1")
	cmd.Env = append(os.Environ(), helperEnv+"=1")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("parallel test with a fake passed:\n%s", out)
	}
	if !strings.Contains(string(out), "t.Parallel") {
		t.Fatalf("child failed for another reason:\n%s", out)
	}
}

// TestParallelFakeHelper is the child half of TestFakeRefusesParallelTest;
// it is skipped unless helperEnv is set.
func TestParallelFakeHelper(t *testing.T) {
	if os.Getenv(helperEnv) != "1" {
		t.Skip("helper for TestFakeRefusesParallelTest")
	}
	t.Parallel()
	FakeSystemctl(t, named("parallel"))
}
