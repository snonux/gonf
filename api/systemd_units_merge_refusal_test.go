package api

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/internal/testutil"
	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
)

// TestSystemdUnitsMergeRefusalsFailFast covers the merges that must be
// refused at registration time with the explicit "cannot merge" error naming
// both watch lists, instead of the generic duplicate-registration refusal or
// a plan that records fine but that no host can apply:
//   - when: a composition inside a when-fragment followed by one after it
//     (the shared reload would be skipped wherever the fragment is inactive);
//   - cycle: the second composition's input depends on the first
//     composition, which contains the reload (merged, the reload would
//     depend on that input: a dependency cycle);
//   - self-fanin / self-dependson: the earlier composition itself is passed
//     to FanIn or DependsOn, so the merged reload would depend on itself;
//   - nested-privileged: the reload was registered by a Privileged nested
//     Run and the unprivileged caller composes again afterwards (the merged
//     op would belong to neither privilege chunk).
//
// A refused merge is a declaration error, so RecordPlan returns it; each
// case records in-process.
func TestSystemdUnitsMergeRefusalsFailFast(t *testing.T) {
	cases := map[string]string{
		"when":              "when_end boundary",
		"cycle":             "would form a cycle",
		"nested-privileged": "different privilege chunks",
		"self-fanin":        "make DaemonReload[system] depend on itself",
		"self-dependson":    "make DaemonReload[system] depend on itself",
	}
	for name, reason := range cases {
		t.Run(name, func(t *testing.T) {
			err := recordMergeRefusalCase(t, name)
			if err == nil {
				t.Fatalf("case %s recorded without a merge refusal", name)
			}
			for _, want := range []string{
				"DaemonReload[system]: cannot merge",
				"File[/etc/systemd/system/a.service]",
				"File[/etc/systemd/system/b.service]",
				reason,
			} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("case %s error misses %q: %v", name, want, err)
				}
			}
			if strings.Contains(err.Error(), "already registered") {
				t.Fatalf("got the generic duplicate refusal instead of the merge error: %v", err)
			}
		})
	}
}

// recordMergeRefusalCase records the named refusal case of
// TestSystemdUnitsMergeRefusalsFailFast and returns the record error.
func recordMergeRefusalCase(t *testing.T, name string) error {
	t.Helper()
	dir := twoUnitSources(t)
	aPath, bPath := "/etc/systemd/system/a.service", "/etc/systemd/system/b.service"
	switch name {
	case "when":
		return systemdUnitsRecordErr(t, func() {
			WhenPathExists(dir, func() {
				a := InstallFile(aPath, filepath.Join(dir, "a.service"))
				SystemdUnits(FanIn(a), ActivateTimer("a"))
			})
			b := InstallFile(bPath, filepath.Join(dir, "b.service"))
			SystemdUnits(FanIn(b), ActivateTimer("b"))
		})
	case "nested-privileged":
		// The outer body's own FanIn is a, the privileged inner one's is b;
		// the inner reload is the one the outer composition finds.
		_, err := recordTwoTasksErr(t,
			func() {
				a := InstallFile(aPath, filepath.Join(dir, "a.service"))
				_ = Run("probe_inner")
				SystemdUnits(FanIn(a), ActivateService("a"))
			},
			func() {
				b := InstallFile(bPath, filepath.Join(dir, "b.service"))
				SystemdUnits(FanIn(b), ActivateService("b"))
			})
		return err
	default: // cycle, self-fanin, self-dependson
		return systemdUnitsRecordErr(t, func() {
			a := InstallFile(aPath, filepath.Join(dir, "a.service"))
			unitsA := SystemdUnits(FanIn(a), ActivateTimer("a"))
			declareAfterComposition(name, unitsA, bPath, filepath.Join(dir, "b.service"))
		})
	}
}

// systemdUnitsRecordErr is systemdUnitsFixture for a body whose record must
// fail: it returns the RecordPlan error instead of failing the test.
func systemdUnitsRecordErr(t *testing.T, body func()) error {
	t.Helper()
	ResetForTest()
	t.Cleanup(ResetForTest)
	RegisterMethods(systemdUnitsTasks{body: body}, WithPrefix("demo_"))
	_, err := RecordPlan("units", testutil.PrivateTempDir(t), "demo_units")
	return err
}

// declareAfterComposition makes the second same-bus declaration of the
// sequenced refusal cases, each of which makes the merged reload depend on
// itself through unitsA (an earlier composition containing the reload):
//   - cycle: the input b depends on unitsA;
//   - self-fanin: unitsA itself is a FanIn input;
//   - self-dependson: an explicit DaemonReload depends on unitsA.
func declareAfterComposition(name string, unitsA Resource, bPath, bSrc string) {
	switch name {
	case "cycle":
		b := InstallFile(bPath, bSrc, options.DependsOn(unitsA))
		SystemdUnits(FanIn(b), ActivateTimer("b"))
	case "self-fanin":
		b := InstallFile(bPath, bSrc)
		SystemdUnits(FanIn(b, unitsA), ActivateTimer("b"))
	case "self-dependson":
		b := InstallFile(bPath, bSrc)
		DaemonReload(options.OnChange(b), options.DependsOn(unitsA))
	}
}

// recordTwoTasks registers outer (unprivileged) and probe_inner (Privileged)
// and records outer.
func recordTwoTasks(t *testing.T, outer, inner func()) []plan.Op {
	t.Helper()
	ops, err := recordTwoTasksErr(t, outer, inner)
	if err != nil {
		t.Fatalf("RecordPlan: %v", err)
	}
	return ops
}

// recordTwoTasksErr is recordTwoTasks returning the record error.
func recordTwoTasksErr(t *testing.T, outer, inner func()) ([]plan.Op, error) {
	t.Helper()
	ResetTasks()
	resource.ResetRepository()
	t.Cleanup(func() {
		resource.SetPlanDraftRecorder(nil)
		plan.SetRecording(false)
		plan.ResetRecord()
	})
	Task("probe_outer", "outer", outer)
	Task("probe_inner", "inner", inner, Privileged())
	return RecordPlan("n", testutil.PrivateTempDir(t), "probe_outer")
}

// TestSystemdUnitsPrivilegedMergeKeepsElevate: two compositions in one
// Privileged task merge into a reload that stays elevated, because the
// amended op is lowered through the same privilege derivation as a recorded
// one (reviewer probe).
func TestSystemdUnitsPrivilegedMergeKeepsElevate(t *testing.T) {
	dir := twoUnitSources(t)
	ResetTasks()
	resource.ResetRepository()
	t.Cleanup(func() {
		resource.SetPlanDraftRecorder(nil)
		plan.SetRecording(false)
		plan.ResetRecord()
	})
	Task("probe_priv", "p", func() {
		a := InstallFile("/etc/systemd/system/a.service", filepath.Join(dir, "a.service"))
		SystemdUnits(FanIn(a), ActivateTimer("a"))
		b := InstallFile("/etc/systemd/system/b.service", filepath.Join(dir, "b.service"))
		SystemdUnits(FanIn(b), ActivateTimer("b"))
	}, Privileged())
	ops, err := RecordPlan("p", testutil.PrivateTempDir(t), "probe_priv")
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	reloads := opsByKind(ops, plan.KindDaemonReload)
	if len(reloads) != 1 || !reloads[0].Elevate || len(reloads[0].Watch) != 2 {
		t.Fatalf("reloads = %#v, want one elevated merged reload", reloads)
	}
}
