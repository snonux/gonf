package resource_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/snonux/gonf/internal/logger"
	"github.com/snonux/gonf/internal/testutil"
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

var _ resource.Action = fakeAction{}

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

// convergeCase is one TestConverge row; actions are action names, and the
// name "fail" fails with the case's boom error.
type convergeCase struct {
	name     string
	actions  []string
	held     bool
	dryRun   bool
	wantErr  bool
	wantDone []string
	wantLog  []string
	wantSum  string
}

// convergeCases covers idle and held notes, dry-run logging without
// performing, in-order execution with its log lines, and the negative path.
func convergeCases() []convergeCase {
	return []convergeCase{
		{name: "nil actions are ok", wantSum: sumOK},
		{name: "no actions held are skipped", held: true, wantSum: sumSkipped},
		{name: "no actions held in dry-run are skipped", held: true, dryRun: true, wantSum: sumSkipped},
		{name: "runs in order and logs did lines", actions: []string{"a", "b"},
			wantDone: []string{"a", "b"}, wantLog: []string{"did a", "did b"}, wantSum: sumChanged},
		{name: "dry-run only logs would lines", actions: []string{"a", "b"}, dryRun: true,
			wantLog: []string{"dry-run: would do a", "dry-run: would do b"}, wantSum: sumWould},
		{name: "first failure aborts the rest", actions: []string{"a", "fail", "c"}, wantErr: true,
			wantDone: []string{"a", "fail"}, wantLog: []string{"did a"}, wantSum: sumNone},
		{name: "dry-run never performs a failing action", actions: []string{"fail"}, dryRun: true,
			wantLog: []string{"dry-run: would do fail"}, wantSum: sumWould},
	}
}

// TestConverge pins the shared action runner, including that the first
// failure is returned unwrapped (identity, not errors.Is) and leaves nothing
// noted.
func TestConverge(t *testing.T) {
	oldDry := resource.DryRun()
	t.Cleanup(func() { resource.SetDryRun(oldDry) })
	boom := errors.New("boom")

	for _, tt := range convergeCases() {
		t.Run(tt.name, func(t *testing.T) {
			resource.ResetReport()
			resource.SetDryRun(tt.dryRun)
			output := testutil.CaptureLog(t, logger.LevelInfo)

			var done []string
			var actions []resource.Action
			for _, n := range tt.actions {
				a := fakeAction{name: n, done: &done}
				if n == "fail" {
					a.err = boom
				}
				actions = append(actions, a)
			}

			err := resource.Converge("Fake[x]", actions, tt.held)
			var wantErr error
			if tt.wantErr {
				wantErr = boom
			}
			if err != wantErr {
				t.Fatalf("err = %v, want %v", err, wantErr)
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

// TestLogLinesSharesMutatePrefix pins that Converge's dry-run line and
// Mutate's use the same "dry-run: would " wording.
func TestLogLinesSharesMutatePrefix(t *testing.T) {
	oldDry := resource.DryRun()
	t.Cleanup(func() { resource.SetDryRun(oldDry) })
	resource.SetDryRun(true)
	resource.ResetReport()
	output := testutil.CaptureLog(t, logger.LevelInfo)

	would, did := resource.LogLines(fakeAction{name: "x"})
	if would != "dry-run: would do x" || did != "did x" {
		t.Fatalf("LogLines = %q, %q", would, did)
	}
	if err := resource.Mutate("Fake[m]", "do x", func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	assertLogLines(t, output(), []string{would})
}

// summaryLine returns the counts line of the current report.
func summaryLine(t *testing.T) string {
	t.Helper()
	var buf strings.Builder
	resource.PrintSummary(&buf)
	line, _, _ := strings.Cut(buf.String(), "\n")
	return line
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
