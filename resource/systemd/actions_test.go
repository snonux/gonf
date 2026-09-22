package systemd

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/snonux/gonf/internal/logger"
	"github.com/snonux/gonf/resource"
)

// Summary lines PrintSummary emits for the single resource Converge notes.
const (
	sumNone    = "summary: 0 ok, 0 changed, 0 skipped, 0 would-change"
	sumOK      = "summary: 1 ok, 0 changed, 0 skipped, 0 would-change"
	sumChanged = "summary: 0 ok, 1 changed, 0 skipped, 0 would-change"
	sumSkipped = "summary: 0 ok, 0 changed, 1 skipped, 0 would-change"
	sumWould   = "summary: 0 ok, 0 changed, 0 skipped, 1 would-change"
)

// fakeAction records Do calls into done and fails with err when set.
type fakeAction struct {
	name string
	err  error
	done *[]string
}

func (a fakeAction) Do() error {
	*a.done = append(*a.done, a.name)
	return a.err
}

func (a fakeAction) Describe() (would, did string) { return "do " + a.name, "did " + a.name }

// summaryLine returns the counts line of the current report.
func summaryLine(t *testing.T) string {
	t.Helper()
	var buf strings.Builder
	resource.PrintSummary(&buf)
	line, _, _ := strings.Cut(buf.String(), "\n")
	return line
}

// TestConverge pins the shared runner: idle and held notes, dry-run logging
// without performing, in-order execution with its log lines, and the
// negative path where the first failure aborts the rest, is returned
// unwrapped and leaves nothing noted.
func TestConverge(t *testing.T) {
	oldDry := resource.DryRun()
	t.Cleanup(func() { resource.SetDryRun(oldDry) })
	boom := errors.New("boom")

	tests := []struct {
		name     string
		actions  []string // action names; "fail" fails with boom
		held     bool
		dryRun   bool
		wantErr  error
		wantDone []string
		wantLog  []string
		wantSum  string
	}{
		{name: "nil actions are ok", wantSum: sumOK},
		{name: "no actions held are skipped", held: true, wantSum: sumSkipped},
		{name: "no actions held in dry-run are skipped", held: true, dryRun: true, wantSum: sumSkipped},
		{name: "runs in order and logs did lines", actions: []string{"a", "b"},
			wantDone: []string{"a", "b"}, wantLog: []string{"did a", "did b"}, wantSum: sumChanged},
		{name: "dry-run only logs would lines", actions: []string{"a", "b"}, dryRun: true,
			wantLog: []string{"dry-run: would do a", "dry-run: would do b"}, wantSum: sumWould},
		{name: "first failure aborts the rest", actions: []string{"a", "fail", "c"}, wantErr: boom,
			wantDone: []string{"a", "fail"}, wantLog: []string{"did a"}, wantSum: sumNone},
		{name: "dry-run never performs a failing action", actions: []string{"fail"}, dryRun: true,
			wantLog: []string{"dry-run: would do fail"}, wantSum: sumWould},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resource.ResetReport()
			resource.SetDryRun(tt.dryRun)
			output, restore := logger.CaptureForTest(logger.LevelInfo)
			defer restore()

			var done []string
			var actions []Action
			for _, n := range tt.actions {
				a := fakeAction{name: n, done: &done}
				if n == "fail" {
					a.err = boom
				}
				actions = append(actions, a)
			}

			err := Converge("Fake[x]", actions, tt.held)
			// Identity, not errors.Is: the runner must return the error unwrapped.
			if err != tt.wantErr {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if !slices.Equal(done, tt.wantDone) {
				t.Errorf("done = %v, want %v", done, tt.wantDone)
			}
			assertLogLines(t, output(), tt.wantLog)
			if got := summaryLine(t); got != tt.wantSum {
				t.Errorf("summary = %q, want %q", got, tt.wantSum)
			}
		})
	}
}

// assertLogLines checks that the captured log holds exactly want, in order.
func assertLogLines(t *testing.T, log string, want []string) {
	t.Helper()
	var got []string
	for line := range strings.SplitSeq(strings.TrimSpace(log), "\n") {
		if line != "" {
			got = append(got, line)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("log lines = %q, want %q", got, want)
	}
	for i := range want {
		if !strings.Contains(got[i], want[i]) {
			t.Errorf("log line %d = %q, want it to contain %q", i, got[i], want[i])
		}
	}
}

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

// TestConvergeCommandFailureStopsAndNotesNothing drives Converge with real
// Commands over a failing fake systemctl: the failing enable aborts the
// start, its error text is returned, and nothing is noted.
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
	actions := []Action{Command(Args(false, "enable", "a.timer")), Command(Args(false, "start", "a.timer"))}
	err := Converge("Timer[a.timer]", actions, false)
	if err == nil || err.Error() != "systemctl [enable a.timer] failed (exit 1): Permission denied" {
		t.Fatalf("err = %v", err)
	}
	if !slices.Equal(calls, []string{"enable a.timer"}) {
		t.Errorf("calls = %v, want only the failing enable", calls)
	}
	if got := summaryLine(t); got != sumNone {
		t.Errorf("summary = %q, want %q", got, sumNone)
	}
}

// TestLogLines pins the operator-visible systemctl wording shared by Timer,
// Service and DaemonReload, with and without --user.
func TestLogLines(t *testing.T) {
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
		would, did := LogLines(tt.cmd)
		if would != tt.wantWould || did != tt.wantDid {
			t.Errorf("LogLines(%v) = %q, %q; want %q, %q", tt.cmd, would, did, tt.wantWould, tt.wantDid)
		}
	}
}
