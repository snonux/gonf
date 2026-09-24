package systemdtimer

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	opt "github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/internal/runners"
	"github.com/snonux/gonf/resource"
	"github.com/snonux/gonf/resource/systemd"
)

// TestTimerOnlyChangeMarksTheCompositeChanged pins the call site of the
// composite's change detection (task 272): when only the timer unit changes
// (unit files already current, daemon-reload a no-op, the timer merely
// enabled and started), SystemdTimer[job] must report changed, which it can
// only do if it watches the exact ID the Timer resource reports under. The
// converged case (timer already enabled and active) must report unchanged,
// so the assertion cannot pass trivially.
func TestTimerOnlyChangeMarksTheCompositeChanged(t *testing.T) {
	if err := systemd.Require("SystemdTimer"); err != nil {
		t.Skip(err)
	}
	for _, timerConverged := range []bool{false, true} {
		changed := applyWithCurrentUnitFiles(t, timerConverged)
		if want := !timerConverged; changed != want {
			t.Errorf("timer converged=%t: SystemdTimer[job] changed=%t, want %t", timerConverged, changed, want)
		}
	}
}

// applyWithCurrentUnitFiles applies a WithUser timer "job" against a
// temporary HOME whose unit files already hold the desired content, with the
// daemon-reload stubbed out and a fake systemctl whose probes report the
// timer as enabled and active only when timerConverged. It returns whether
// SystemdTimer[job] reported a change, after checking that the unit files
// and the daemon-reload did not.
func applyWithCurrentUnitFiles(t *testing.T, timerConverged bool) bool {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)

	var ran [][]string
	sysR := &runners.SystemdRunners{Run: func(_ string, args ...string) (string, string, int, error) {
		ran = append(ran, args)
		if !timerConverged && (slices.Contains(args, "is-active") || slices.Contains(args, "is-enabled")) {
			return "", "", 1, nil
		}
		return "", "", 0, nil
	}}
	tm := newTimerWith(sysR, "job", testTimerOpts(true)...)
	dir := filepath.Join(home, ".config/systemd/user")
	writeUnit(t, filepath.Join(dir, "job.service"), tm.serviceUnit())
	writeUnit(t, filepath.Join(dir, "job.timer"), tm.timerUnit())

	ensureReload = func(*runners.SystemdRunners, ...opt.DaemonReloadOption) error { return nil }
	t.Cleanup(func() { ensureReload = systemd.EnsureWith })

	resource.ResetReport()
	t.Cleanup(resource.ResetReport)
	if err := tm.apply(); err != nil {
		t.Fatalf("apply: %v (systemctl calls %q)", err, ran)
	}
	unitFiles := []string{resource.FormatID("File", filepath.Join(dir, "job.service")), resource.FormatID("File", filepath.Join(dir, "job.timer"))}
	if resource.AnyChanged(unitFiles...) || resource.AnyChanged(daemonReloadID(true)) {
		t.Fatalf("unit files or daemon-reload reported a change; the test must isolate the timer")
	}
	if timerChanged := resource.AnyChanged("Timer[job.timer]"); timerChanged == timerConverged {
		t.Fatalf("timer converged=%t but Timer[job.timer] changed=%t (systemctl calls %q)", timerConverged, timerChanged, ran)
	}
	return resource.AnyChanged("SystemdTimer[job]")
}

// writeUnit writes content at path with mode 0644 (as the composite
// installs it), creating the parent directory.
func writeUnit(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil { // independent of the umask
		t.Fatal(err)
	}
}
