package remote

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/snonux/gonf/internal"
)

// guardTarget uses the reserved .invalid TLD so that, should the guard ever
// regress, the real ssh/scp it lets through fails name resolution instead of
// reaching a real host.
var guardTarget = PushTarget{Host: "guard.invalid"}

// requireNetworkExecRefused runs fn and fails the test unless it panics with
// the network-exec guard's message: the guard must stop a real ssh/scp
// before any process is started.
func requireNetworkExecRefused(t *testing.T, seam string, fn func()) {
	t.Helper()
	defer func() {
		r := recover()
		if r == nil {
			t.Fatalf("%s: real ssh/scp exec was not refused inside a test binary", seam)
		}
		if msg := fmt.Sprint(r); !strings.Contains(msg, "un-faked") {
			t.Fatalf("%s: unexpected panic %q, want the network-exec guard's message", seam, msg)
		}
	}()
	fn()
}

// TestDefaultNetworkRunnersRefuseInTests pins the guard on every production
// seam that would exec ssh or scp: the streaming runner (SSHRunner's
// default), the capturing runner behind every version/uname/mktemp probe
// (sshCaptureExec's default) and the binary-staging copier (Pusher's default
// SCPRunner). The named defaults are called directly, so this test never
// races with the tests that swap the package-level seams.
func TestDefaultNetworkRunnersRefuseInTests(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	requireNetworkExecRefused(t, "defaultSSHRunner", func() {
		_ = defaultSSHRunner(ctx, nil, guardTarget.sshArgv("true"))
	})
	requireNetworkExecRefused(t, "defaultSSHCaptureExec", func() {
		_, _, _ = defaultSSHCaptureExec(ctx, guardTarget.sshArgv("true"))
	})
	requireNetworkExecRefused(t, "defaultSCPRunner", func() {
		_ = defaultSCPRunner(ctx, os.DevNull, guardTarget, "/tmp/gonf")
	})
}

// TestNetworkExecGuardIgnoresLocalCommands keeps the guard narrow: tests such
// as TestSSHRunnerKillsOnContextDeadline drive the real runner with a local
// command ("sleep") on purpose, and that must keep working.
func TestNetworkExecGuardIgnoresLocalCommands(t *testing.T) {
	t.Parallel()
	for _, argv := range [][]string{{"sleep", "0"}, {"/bin/true"}, {"sshfs"}} {
		refuseNetworkExecInTests(argv) // must not panic
	}
	for _, argv := range [][]string{{"ssh"}, {"/usr/bin/ssh", "h"}, {"scp", "a", "b"}} {
		requireNetworkExecRefused(t, strings.Join(argv, " "), func() { refuseNetworkExecInTests(argv) })
	}
}

// TestAssumeRemotePlanCurrentFakesEveryPushProbe is the regression test for
// x72: AssumeRemotePlanCurrent used to fake only the plan-schema probe, so
// EnsureRemoteGonf still ran the real release-version probe ("gonf
// -version" over ssh) against the fake *.example hosts of every test that
// used it. With the helper installed, EnsureRemoteGonf must decide "no
// upgrade" without a single ssh capture. Not parallel: it swaps the
// package-level sshCaptureExec and defaultPusher probes.
func TestAssumeRemotePlanCurrentFakesEveryPushProbe(t *testing.T) {
	restore := AssumeRemotePlanCurrent()
	t.Cleanup(restore)
	oldCapture := sshCaptureExec
	t.Cleanup(func() { sshCaptureExec = oldCapture })
	var captured []string
	sshCaptureExec = func(_ context.Context, argv []string) (string, string, error) {
		captured = append(captured, argv[len(argv)-1])
		return internal.Version + "\n", "", nil
	}

	installed, err := EnsureRemoteGonf(context.Background(), PushTarget{Host: "h.example"})
	if err != nil {
		t.Fatalf("EnsureRemoteGonf: %v", err)
	}
	if installed != "" {
		t.Fatalf("EnsureRemoteGonf installed %q, want no upgrade", installed)
	}
	if len(captured) != 0 {
		t.Fatalf("AssumeRemotePlanCurrent left real ssh probes running: %q", captured)
	}
}

// funcPtr identifies a func value, so a test can tell which probe is
// installed in a seam.
func funcPtr(f any) uintptr { return reflect.ValueOf(f).Pointer() }

// TestAssumeSeamsRestoreEveryProbe: the restore funcs put back every probe
// the seams replaced. A leaked fake would stay installed for the rest of the
// package's tests, and a later test of the real staleness path would pass
// for the wrong reason.
func TestAssumeSeamsRestoreEveryProbe(t *testing.T) {
	before := [3]uintptr{
		funcPtr(defaultPusher.PlanVersionProber),
		funcPtr(defaultPusher.ReleaseVersionProber),
		funcPtr(defaultPusher.StrictPreviewProber),
	}
	for name, assume := range map[string]func() func(){
		"AssumeRemotePlanCurrent": AssumeRemotePlanCurrent,
		"AssumeRemoteGonfCurrent": AssumeRemoteGonfCurrent,
	} {
		assume()()
		after := [3]uintptr{
			funcPtr(defaultPusher.PlanVersionProber),
			funcPtr(defaultPusher.ReleaseVersionProber),
			funcPtr(defaultPusher.StrictPreviewProber),
		}
		if after != before {
			t.Fatalf("%s restore left probes %v, want the originals %v (plan, release, strict preview)", name, after, before)
		}
	}
}

// TestNetworkExecGuardOnlyInGonfTestBinaries: the guard is scoped to gonf's
// own test binaries (this one), not to every test binary that links gonf.
func TestNetworkExecGuardOnlyInGonfTestBinaries(t *testing.T) {
	if !inGonfTestBinary() {
		t.Fatal("inGonfTestBinary() = false inside gonf's own test binary")
	}
}
