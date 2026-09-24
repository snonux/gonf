package systemd

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/snonux/gonf/resource"
)

// TestCommandRunsSystemctl pins Command against a fake systemctl injected
// directly through Client.Command (task 4e2: no process-global fake): the
// argv (including --user from Args) reaches systemctl unchanged, and a
// non-zero exit or a start failure is returned with Client.Run's error text.
func TestCommandRunsSystemctl(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		stderr  string
		code    int
		runErr  error
		wantErr string
	}{
		{name: "system bus", args: Args(false, "restart", "a.timer")},
		{name: "user bus", args: Args(true, "enable", "a.timer")},
		{name: "non-zero exit", args: Args(false, "start", "a.timer"), stderr: "denied", code: 1,
			wantErr: "systemctl [start a.timer] failed (exit 1): denied"},
		{name: "start failure", args: Args(true, "stop", "a.timer"), runErr: errors.New("not found"),
			wantErr: "systemctl [--user stop a.timer]: not found"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var saw []string
			fake := func(name string, args ...string) (string, string, int, error) {
				saw = append([]string{name}, args...)
				return "", tt.stderr, tt.code, tt.runErr
			}
			cmd := (Client{run: fake}).Command(tt.args)
			err := cmd.Do()
			if want := append([]string{"systemctl"}, tt.args...); !slices.Equal(saw, want) {
				t.Errorf("argv = %v, want %v", saw, want)
			}
			switch {
			case tt.wantErr == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case tt.wantErr != "" && (err == nil || err.Error() != tt.wantErr):
				t.Fatalf("err = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

// TestConvergeCommandFailureStopsAndNotesNothing drives resource.Converge
// with real Commands (from a faked Client) over a failing fake systemctl:
// the failing enable aborts the start, its error text is returned, and
// nothing is noted.
func TestConvergeCommandFailureStopsAndNotesNothing(t *testing.T) {
	oldDry := resource.DryRun()
	t.Cleanup(func() { resource.SetDryRun(oldDry) })
	resource.SetDryRun(false)
	resource.ResetReport()

	var calls []string
	fake := func(name string, args ...string) (string, string, int, error) {
		calls = append(calls, strings.Join(args, " "))
		return "", "Permission denied", 1, nil
	}
	c := Client{run: fake}
	actions := []resource.Action{c.Command(Args(false, "enable", "a.timer")), c.Command(Args(false, "start", "a.timer"))}
	err := resource.Converge("Timer[a.timer]", actions, false)
	if err == nil || err.Error() != "systemctl [enable a.timer] failed (exit 1): Permission denied" {
		t.Fatalf("err = %v", err)
	}
	if !slices.Equal(calls, []string{"enable a.timer"}) {
		t.Errorf("calls = %v, want only the failing enable", calls)
	}
	var buf strings.Builder
	resource.PrintSummary(&buf)
	if want := "summary: 0 ok, 0 changed, 0 skipped, 0 would-change\n"; buf.String() != want {
		t.Errorf("summary = %q, want %q (nothing noted)", buf.String(), want)
	}
}

// TestCommandLogLines pins the operator-visible systemctl wording shared by
// Timer, Service and DaemonReload, with and without --user. The runner never
// runs here, so a bare Client{}.Command is fine.
func TestCommandLogLines(t *testing.T) {
	tests := []struct {
		args      []string
		wantWould string
		wantDid   string
	}{
		{Args(false, "restart", "sshd"), "dry-run: would run systemctl [restart sshd]", "systemctl [restart sshd]"},
		{Args(true, "enable", "a.timer"), "dry-run: would run systemctl [--user enable a.timer]", "systemctl [--user enable a.timer]"},
		{Args(false, "daemon-reload"), "dry-run: would run systemctl [daemon-reload]", "systemctl [daemon-reload]"},
	}
	for _, tt := range tests {
		cmd := (Client{}).Command(tt.args)
		would, did := resource.LogLines(cmd)
		if would != tt.wantWould || did != tt.wantDid {
			t.Errorf("LogLines(%v) = %q, %q; want %q, %q", tt.args, would, did, tt.wantWould, tt.wantDid)
		}
	}
}
