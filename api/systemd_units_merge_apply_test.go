package api

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
	"github.com/snonux/gonf/resource/systemd"
)

// mergedApplyFixture records two system compositions whose inputs install
// from src into dst, with WithRestart timers a and b.
type mergedApplyFixture struct {
	src, dst string
	// lastInput is the file the later declaration installs; daemon-reload
	// must never run before it exists.
	lastInput string
	invoked   [][]string
}

// newMergedApplyFixture skips off systemd hosts, prepares the sources and
// stubs systemctl, recording every invocation. The stub flags a
// daemon-reload that runs before lastInput (default: dst/b.service) exists.
func newMergedApplyFixture(t *testing.T) *mergedApplyFixture {
	t.Helper()
	if runtime.GOOS != "linux" || !systemd.Detected() {
		t.Skip("systemd apply fake is Linux-specific")
	}
	f := &mergedApplyFixture{src: twoUnitSources(t), dst: t.TempDir()}
	f.lastInput = filepath.Join(f.dst, "b.service")
	systemd.SetRunCmdForTest(func(name string, args ...string) (string, string, int, error) {
		if name != "systemctl" {
			return "", "", 0, nil
		}
		f.invoked = append(f.invoked, args)
		if args[0] == "daemon-reload" {
			if _, err := os.Stat(f.lastInput); err != nil {
				t.Errorf("daemon-reload ran before the later declaration's input %s was installed", f.lastInput)
			}
		}
		return "", "", 0, nil
	})
	t.Cleanup(systemd.ResetRunCmdForTest)
	return f
}

// apply records two compositions from the current sources and applies the
// plan.
func (f *mergedApplyFixture) apply(t *testing.T) {
	t.Helper()
	f.applyBody(t, func() {
		a := InstallFile(filepath.Join(f.dst, "a.service"), filepath.Join(f.src, "a.service"))
		SystemdUnits(FanIn(a), ActivateTimer("a", options.WithRestart))
		b := InstallFile(filepath.Join(f.dst, "b.service"), filepath.Join(f.src, "b.service"))
		SystemdUnits(FanIn(b), ActivateTimer("b", options.WithRestart))
	})
}

// applyBody records body as the task body and applies the plan.
func (f *mergedApplyFixture) applyBody(t *testing.T, body func()) {
	t.Helper()
	ops := systemdUnitsFixture(t, body)
	f.invoked = nil
	if err := plan.Apply(ops, plan.Facts{GOOS: runtime.GOOS}, ""); err != nil {
		t.Fatalf("plan.Apply: %v", err)
	}
}

func (f *mergedApplyFixture) count(verb string) int {
	n := 0
	for _, args := range f.invoked {
		if args[0] == verb {
			n++
		}
	}
	return n
}

func (f *mergedApplyFixture) restarted(unit string) bool {
	for _, args := range f.invoked {
		if args[0] == "restart" && args[len(args)-1] == unit {
			return true
		}
	}
	return false
}

// TestSystemdUnitsMergedReloadRunsOnceAfterAllInputs: the merged reload op
// keeps its first recorded position, yet apply runs it exactly once and
// only after the second composition's input is installed (the stub checks
// the order).
func TestSystemdUnitsMergedReloadRunsOnceAfterAllInputs(t *testing.T) {
	f := newMergedApplyFixture(t)
	f.apply(t)
	if n := f.count("daemon-reload"); n != 1 {
		t.Fatalf("daemon-reload ran %d times, want 1: %v", n, f.invoked)
	}
}

// TestSystemdUnitsMergedReloadFiresOnSecondInput: a later change of only the
// second composition's input still reloads systemd (the watch the merge
// adds) and restarts only that composition's timer.
func TestSystemdUnitsMergedReloadFiresOnSecondInput(t *testing.T) {
	f := newMergedApplyFixture(t)
	f.apply(t)
	writeFixtureFile(t, filepath.Join(f.src, "b.service"), "[Unit]\nDescription=b2\n")
	f.apply(t)
	if n := f.count("daemon-reload"); n != 1 {
		t.Fatalf("change of the second input ran daemon-reload %d times, want 1: %v", n, f.invoked)
	}
	if f.restarted("a.timer") {
		t.Fatalf("unchanged composition restarted: %v", f.invoked)
	}
	if !f.restarted("b.timer") {
		t.Fatalf("changed composition did not restart b.timer: %v", f.invoked)
	}
}

// TestSystemdUnitsMergedWatchOnlyDeclarationOrdersReload is the regression
// test for a later declaration that watches an input without depending on it
// (WithWatch + IfChanged; WatchChanges behaves the same). The merged reload
// keeps the composition's earlier recorded position, so it must still be
// ordered after the watched input: otherwise it runs before b is written,
// sees no change and skips, and a change of only b never reloads systemd.
func TestSystemdUnitsMergedWatchOnlyDeclarationOrdersReload(t *testing.T) {
	f := newMergedApplyFixture(t)
	bPath := filepath.Join(f.dst, "b.service")
	body := func() {
		a := InstallFile(filepath.Join(f.dst, "a.service"), filepath.Join(f.src, "a.service"))
		SystemdUnits(FanIn(a), ActivateTimer("a"))
		InstallFile(bPath, filepath.Join(f.src, "b.service"))
		DaemonReload(options.WithWatch("File["+bPath+"]"), options.IfChanged)
	}
	f.applyBody(t, body)
	writeFixtureFile(t, filepath.Join(f.src, "b.service"), "[Unit]\nDescription=b2\n")
	f.applyBody(t, body)
	if n := f.count("daemon-reload"); n != 1 {
		t.Fatalf("change of only the watched input ran daemon-reload %d times, want 1: %v", n, f.invoked)
	}
}

// TestSystemdUnitsMergedDirectoryWatchOrdersReloadAfterFiles is the
// regression test for a later declaration watching a Directory[p]: the
// change gate also fires on File[p/…] notes (AnyChanged), so the merged
// reload, which keeps the composition's earlier position, must be ordered
// after the files under p and not just after the directory. Otherwise it
// runs between the directory and its file, and a change of only the file
// never reloads systemd. Both the OnChange form and the legacy
// DependsOn + IfChanged form are covered.
func TestSystemdUnitsMergedDirectoryWatchOrdersReloadAfterFiles(t *testing.T) {
	forms := map[string]func(d Resource) Resource{
		"onchange": func(d Resource) Resource { return DaemonReload(options.OnChange(d)) },
		"legacy":   func(d Resource) Resource { return DaemonReload(options.DependsOn(d), options.IfChanged) },
	}
	for name, declare := range forms {
		t.Run(name, func(t *testing.T) {
			f := newMergedApplyFixture(t)
			sub := filepath.Join(f.dst, "sub")
			f.lastInput = filepath.Join(sub, "x.conf")
			body := func() {
				a := InstallFile(filepath.Join(f.dst, "a.service"), filepath.Join(f.src, "a.service"))
				SystemdUnits(FanIn(a), ActivateTimer("a"))
				d := Dir(sub)
				InstallFile(f.lastInput, filepath.Join(f.src, "b.service"), options.DependsOn(d))
				declare(d)
			}
			f.applyBody(t, body)
			writeFixtureFile(t, filepath.Join(f.src, "b.service"), "[Unit]\nDescription=x2\n")
			f.applyBody(t, body)
			if n := f.count("daemon-reload"); n != 1 {
				t.Fatalf("change of only the file under the watched directory ran daemon-reload %d times, want 1: %v", n, f.invoked)
			}
		})
	}
}

// TestSystemdUnitsMergeOutsideRecordingUpdatesRegisteredDraft covers the
// api.Apply path, which lowers the registered drafts instead of a recorded
// plan: the stored reload draft carries the merged watch list.
func TestSystemdUnitsMergeOutsideRecordingUpdatesRegisteredDraft(t *testing.T) {
	dir := twoUnitSources(t)
	resource.ResetRepository()
	t.Cleanup(resource.ResetRepository)
	a := InstallFile("/etc/systemd/system/a.service", filepath.Join(dir, "a.service"))
	SystemdUnits(FanIn(a), ActivateTimer("a"))
	b := InstallFile("/etc/systemd/system/b.service", filepath.Join(dir, "b.service"))
	SystemdUnits(FanIn(b), ActivateTimer("b"))

	want := []string{"File[/etc/systemd/system/a.service]", "File[/etc/systemd/system/b.service]"}
	for _, d := range resource.RegisteredPlanDrafts() {
		if d.ID == "DaemonReload[system]" {
			if !reflect.DeepEqual(d.Watch, want) || !reflect.DeepEqual(d.Deps, want) {
				t.Fatalf("registered reload draft = %#v, want union %v", d, want)
			}
			return
		}
	}
	t.Fatal("no registered DaemonReload[system] draft")
}
