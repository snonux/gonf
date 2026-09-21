package systemdtimer

import (
	"path/filepath"
	"reflect"
	"testing"

	opt "github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/resource"
	"github.com/snonux/gonf/resource/systemd"
)

// reloadDraft resolves daemon-reload options to the draft a registered
// daemon-reload would record, so tests can compare gate, watch list and bus
// instead of opaque option funcs.
func reloadDraft(t *testing.T, opts []opt.DaemonReloadOption) resource.PlanDraft {
	t.Helper()
	resource.ResetRepository()
	t.Cleanup(resource.ResetRepository)
	systemd.Present(opts...)
	for _, d := range resource.RegisteredPlanDrafts() {
		if d.Kind == "daemon_reload" {
			return d
		}
	}
	t.Fatal("no daemon_reload draft recorded")
	return resource.PlanDraft{}
}

// testTimerOpts is a valid systemd-timer configuration, optionally on the
// user bus.
func testTimerOpts(user bool) []opt.SystemdTimerOption {
	opts := []opt.SystemdTimerOption{opt.WithCommand("/bin/true"), opt.WithOnCalendar("daily")}
	if user {
		opts = append(opts, opt.WithUser)
	}
	return opts
}

// TestDaemonReloadOpts pins the daemon-reload the composite configures: the
// legacy IfChanged gate armed, watching exactly the service and timer unit
// files (in that order), on the user bus only for WithUser timers.
func TestDaemonReloadOpts(t *testing.T) {
	for _, user := range []bool{false, true} {
		tm := newTimer("job", testTimerOpts(user)...)
		d := reloadDraft(t, tm.daemonReloadOpts("File[s]", "File[t]"))
		if !d.IfChanged || d.User != user || !reflect.DeepEqual(d.Watch, []string{"File[s]", "File[t]"}) {
			t.Errorf("user=%t: draft IfChanged=%t User=%t Watch=%#v, want true/%t/[File[s] File[t]]",
				user, d.IfChanged, d.User, d.Watch, user)
		}
	}
}

// TestApplyPathsBothEnsureGatedReload pins that the present and the absent
// path each ensure the same gated daemon-reload once, watching the timer's
// own unit files. It runs a WithUser timer against a temporary HOME and a
// fake systemctl, with the reload itself stubbed out.
func TestApplyPathsBothEnsureGatedReload(t *testing.T) {
	if err := systemd.Require("SystemdTimer"); err != nil {
		t.Skip(err)
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	systemd.SetRunCmdForTest(func(string, ...string) (string, string, int, error) { return "", "", 0, nil })
	t.Cleanup(systemd.ResetRunCmdForTest)
	var calls [][]opt.DaemonReloadOption
	ensureReload = func(opts ...opt.DaemonReloadOption) error {
		calls = append(calls, opts)
		return nil
	}
	t.Cleanup(func() { ensureReload = systemd.Ensure })

	dir := filepath.Join(home, ".config/systemd/user")
	wantWatch := []string{"File[" + filepath.Join(dir, "job.service") + "]", "File[" + filepath.Join(dir, "job.timer") + "]"}
	for _, absent := range []bool{false, true} {
		calls = nil
		opts := testTimerOpts(true)
		if absent {
			opts = append(opts, opt.IsAbsent)
		}
		resource.ResetReport()
		if err := newTimer("job", opts...).apply(); err != nil {
			t.Fatalf("absent=%t apply: %v", absent, err)
		}
		if len(calls) != 1 {
			t.Fatalf("absent=%t: daemon-reload ensured %d times, want 1", absent, len(calls))
		}
		d := reloadDraft(t, calls[0])
		if !d.IfChanged || !d.User || !reflect.DeepEqual(d.Watch, wantWatch) {
			t.Errorf("absent=%t: draft IfChanged=%t User=%t Watch=%#v, want true/true/%#v",
				absent, d.IfChanged, d.User, d.Watch, wantWatch)
		}
	}
}
