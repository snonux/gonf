package systemd

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/snonux/gonf/resource"
)

// TestCommandRunsSystemctl pins Command against a fake systemctl: the argv
// (including --user from Args) reaches systemctl unchanged, and a non-zero
// exit or a start failure is returned with Run's error text.
func TestCommandRunsSystemctl(t *testing.T) {
	old := runCmd
	t.Cleanup(func() { runCmd = old })

	tests := []struct {
		name    string
		cmd     Command
		stderr  string
		code    int
		runErr  error
		wantErr string
	}{
		{name: "system bus", cmd: Command(Args(false, "restart", "a.timer"))},
		{name: "user bus", cmd: Command(Args(true, "enable", "a.timer"))},
		{name: "non-zero exit", cmd: Command(Args(false, "start", "a.timer")), stderr: "denied", code: 1,
			wantErr: "systemctl [start a.timer] failed (exit 1): denied"},
		{name: "start failure", cmd: Command(Args(true, "stop", "a.timer")), runErr: errors.New("not found"),
			wantErr: "systemctl [--user stop a.timer]: not found"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var saw []string
			runCmd = func(name string, args ...string) (string, string, int, error) {
				saw = append([]string{name}, args...)
				return "", tt.stderr, tt.code, tt.runErr
			}
			err := tt.cmd.Do()
			if want := append([]string{"systemctl"}, tt.cmd...); !slices.Equal(saw, want) {
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
// with real Commands over a failing fake systemctl: the failing enable
// aborts the start, its error text is returned, and nothing is noted.
func TestConvergeCommandFailureStopsAndNotesNothing(t *testing.T) {
	oldDry := resource.DryRun()
	t.Cleanup(func() { resource.SetDryRun(oldDry) })
	resource.SetDryRun(false)
	resource.ResetReport()
	old := runCmd
	t.Cleanup(func() { runCmd = old })

	var calls []string
	runCmd = func(name string, args ...string) (string, string, int, error) {
		calls = append(calls, strings.Join(args, " "))
		return "", "Permission denied", 1, nil
	}
	actions := []resource.Action{Command(Args(false, "enable", "a.timer")), Command(Args(false, "start", "a.timer"))}
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
// Timer, Service and DaemonReload, with and without --user.
func TestCommandLogLines(t *testing.T) {
	tests := []struct {
		cmd       Command
		wantWould string
		wantDid   string
	}{
		{Command(Args(false, "restart", "sshd")), "dry-run: would run systemctl [restart sshd]", "systemctl [restart sshd]"},
		{Command(Args(true, "enable", "a.timer")), "dry-run: would run systemctl [--user enable a.timer]", "systemctl [--user enable a.timer]"},
		{Command(Args(false, "daemon-reload")), "dry-run: would run systemctl [daemon-reload]", "systemctl [daemon-reload]"},
	}
	for _, tt := range tests {
		would, did := resource.LogLines(tt.cmd)
		if would != tt.wantWould || did != tt.wantDid {
			t.Errorf("LogLines(%v) = %q, %q; want %q, %q", tt.cmd, would, did, tt.wantWould, tt.wantDid)
		}
	}
}
