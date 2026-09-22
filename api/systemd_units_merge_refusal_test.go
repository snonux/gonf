package api

import (
	"os"
	"os/exec"
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
// both watch lists, instead of the generic duplicate-registration abort or
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
// logger.Fatal exits, so each case runs in a helper process.
func TestSystemdUnitsMergeRefusalsFailFast(t *testing.T) {
	cases := map[string]string{
		"when":              "when_end boundary",
		"cycle":             "would form a cycle",
		"nested-privileged": "different privilege chunks",
		"self-fanin":        "make DaemonReload[system] depend on itself",
		"self-dependson":    "make DaemonReload[system] depend on itself",
	}
	before := countFatalHelperTempDirs(t)
	for name, reason := range cases {
		t.Run(name, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestSystemdUnitsMergeFatalHelperProcess$", "-test.timeout=60s")
			// The helper builds fixtures with twoUnitSources/systemdUnitsFixture,
			// both of which call t.TempDir(), and then exits via logger.Fatal
			// (os.Exit), which skips its own deferred cleanup. t.TempDir()
			// creates its directory under $GOTMPDIR (see
			// testing.common.makeTempDir), so pointing the child's GOTMPDIR at
			// a directory this (surviving) test owns means that directory -
			// and whatever the child created below it - is removed by this
			// test's own t.TempDir() cleanup instead of leaking under the
			// real $GOTMPDIR/temp root.
			cmd.Env = append(os.Environ(), "GONF_API_UNITS_MERGE_FATAL="+name, "GOTMPDIR="+t.TempDir())
			out, err := cmd.CombinedOutput()
			if err == nil {
				t.Fatalf("case %s exited 0; output:\n%s", name, out)
			}
			for _, want := range []string{
				"DaemonReload[system]: cannot merge",
				"File[/etc/systemd/system/a.service]",
				"File[/etc/systemd/system/b.service]",
				reason,
			} {
				if !strings.Contains(string(out), want) {
					t.Fatalf("case %s output misses %q:\n%s", name, want, out)
				}
			}
			if strings.Contains(string(out), "already registered") {
				t.Fatalf("got the generic duplicate abort instead of the merge error:\n%s", out)
			}
		})
	}
	assertNoNewFatalHelperTempDirs(t, before)
}

// TestSystemdUnitsMergeFatalHelperProcess is the helper process for
// TestSystemdUnitsMergeRefusalsFailFast; it must never exit 0.
func TestSystemdUnitsMergeFatalHelperProcess(t *testing.T) {
	name := os.Getenv("GONF_API_UNITS_MERGE_FATAL")
	if name == "" {
		return
	}
	dir := twoUnitSources(t)
	aPath, bPath := "/etc/systemd/system/a.service", "/etc/systemd/system/b.service"
	switch name {
	case "when":
		systemdUnitsFixture(t, func() {
			WhenPathExists(dir, func() {
				a := InstallFile(aPath, filepath.Join(dir, "a.service"))
				SystemdUnits(FanIn(a), ActivateTimer("a"))
			})
			b := InstallFile(bPath, filepath.Join(dir, "b.service"))
			SystemdUnits(FanIn(b), ActivateTimer("b"))
		})
	case "cycle", "self-fanin", "self-dependson":
		systemdUnitsFixture(t, func() {
			a := InstallFile(aPath, filepath.Join(dir, "a.service"))
			unitsA := SystemdUnits(FanIn(a), ActivateTimer("a"))
			declareAfterComposition(name, unitsA, bPath, filepath.Join(dir, "b.service"))
		})
	case "nested-privileged":
		// The outer body's own FanIn is a, the privileged inner one's is b;
		// the inner reload is the one the outer composition finds.
		recordTwoTasks(t,
			func() {
				a := InstallFile(aPath, filepath.Join(dir, "a.service"))
				_ = Run("probe_inner")
				SystemdUnits(FanIn(a), ActivateService("a"))
			},
			func() {
				b := InstallFile(bPath, filepath.Join(dir, "b.service"))
				SystemdUnits(FanIn(b), ActivateService("b"))
			})
	}
	t.Fatalf("case %q recorded without a merge refusal", name)
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
	ResetTasks()
	resource.ResetRepository()
	t.Cleanup(func() {
		resource.SetPlanDraftRecorder(nil)
		plan.SetRecording(false)
		plan.ResetRecord()
	})
	Task("probe_outer", "outer", outer)
	Task("probe_inner", "inner", inner, Privileged())
	ops, err := RecordPlan("n", testutil.PrivateTempDir(t), "probe_outer")
	if err != nil {
		t.Fatalf("RecordPlan: %v", err)
	}
	return ops
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
