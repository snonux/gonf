package plan_test

// These two package-kind apply tests live in the external plan_test package
// (not plan) because they stub resource/pkg's command runner directly, and
// resource/pkg registers a plan.Handler (see resource/pkg/planwire.go) —
// importing resource/pkg from an internal plan test would be an import
// cycle (plan -> resource/pkg -> plan). Every resource kind now registers a
// plan.Handler (task i5), so any plan test that stubs a resource kind's
// runner directly lives out here instead — see also
// apply_systemd_test.go's TestApplyTimerRestartLowering.

import (
	"testing"

	"github.com/snonux/gonf/internal/testseam"
	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
)

func pkgHeader() plan.Op {
	return plan.Op{Op: plan.KindPlan, Version: plan.CurrentVersion, ID: "apply-test"}
}

func TestApplyPackageDryRun(t *testing.T) {
	resource.SetDryRun(true)
	t.Cleanup(func() { resource.SetDryRun(false) })
	testseam.FakePackageManager(t, func() (string, error) { return "dnf", nil })

	ops := []plan.Op{
		pkgHeader(),
		{Op: plan.KindPackage, Name: "tig"},
	}
	if err := plan.Apply(ops, plan.Facts{}, ""); err != nil {
		t.Fatalf("dry-run package: %v", err)
	}
}

// TestApplyPackageLatestRunsUpgradePath pins the m5 regression fix: a
// package op recorded with Latest:true must reach the backend's
// upgrade-check path (dnf update, here) even when the package already shows
// as installed — not the plain install/no-op path a bare "package" op would
// take. Before the fix, plan/apply.go's applyPackage never looked at
// op.Latest at all, so IsLatest was silently dropped on every plan/push run.
// Since j5, the package kind's apply behavior lives in
// resource/pkg/planwire.go's Handler.Apply; this test still pins the
// observable behavior through the public plan.Apply entry point.
func TestApplyPackageLatestRunsUpgradePath(t *testing.T) {
	testseam.FakePackageManager(t, func() (string, error) { return "dnf", nil })

	var dnfCalls [][]string
	testseam.FakePackageRunner(t, testseam.Package{Run: func(name string, args ...string) (string, string, int, error) {
		switch name {
		case "rpm":
			// Report the package as already installed: a plain "package"
			// op would then be a no-op, but Latest must still act.
			return "rsync-1.0-1\n", "", 0, nil
		case "dnf":
			dnfCalls = append(dnfCalls, args)
			return "", "", 0, nil
		default:
			return "", "unexpected " + name, 1, nil
		}
	}})

	ops := []plan.Op{
		pkgHeader(),
		{Op: plan.KindPackage, Name: "rsync", Latest: true},
	}
	if err := plan.Apply(ops, plan.Facts{}, ""); err != nil {
		t.Fatalf("apply package latest: %v", err)
	}

	var sawUpdate bool
	for _, args := range dnfCalls {
		if len(args) >= 1 && args[0] == "update" {
			sawUpdate = true
		}
	}
	if !sawUpdate {
		t.Errorf("expected a dnf update invocation for the already-installed package, got dnf calls: %v", dnfCalls)
	}
}
