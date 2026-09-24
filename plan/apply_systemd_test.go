package plan_test

// TestApplyTimerRestartLowering lives in the external plan_test package (not
// plan) because it stubs resource/systemd's command runner directly, and
// resource/systemd now registers a plan.Handler (see
// resource/systemd/planwire.go) — importing resource/systemd from an
// internal plan test would be an import cycle (plan -> resource/systemd ->
// plan), mirroring why the package-kind apply tests live in
// apply_pkg_test.go.

import (
	"context"
	"runtime"
	"testing"

	"github.com/snonux/gonf/internal/runners"
	"github.com/snonux/gonf/plan"
)

// TestApplyTimerRestartLowering pins that a recorded timer op with restart
// lowers into a WithRestart option: the fake runner must observe a systemctl
// restart of the unit, not just enable/start (task z12 regression).
func TestApplyTimerRestartLowering(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("systemd fake is Linux-specific")
	}

	var invoked [][]string
	sr := &runners.SystemdRunners{Run: func(name string, args ...string) (string, string, int, error) {
		if name == "systemctl" {
			invoked = append(invoked, args)
		}
		return "", "", 0, nil
	}}

	ops := []plan.Op{
		{Op: plan.KindPlan, Version: plan.CurrentVersion, ID: "timers"},
		{Op: plan.KindTimer, Name: "zzfit.timer", User: true, Restart: true},
	}
	ctx := runners.WithSet(context.Background(), &runners.Set{Systemd: sr})
	if err := plan.ApplyWithContext(ctx, ops, plan.Facts{GOOS: "linux"}, ""); err != nil {
		t.Fatalf("plan.Apply: %v", err)
	}

	var sawRestart bool
	for _, args := range invoked {
		if len(args) >= 2 && args[0] == "--user" && args[1] == "restart" {
			sawRestart = true
		}
	}
	if !sawRestart {
		t.Errorf("expected a systemctl --user restart invocation, got: %v", invoked)
	}
}
