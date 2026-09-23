package file

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/internal/logger"
	"github.com/snonux/gonf/internal/testutil"
	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
)

const (
	pkgKey    = "export PKG_PATH="
	pkgQuoted = `export PKG_PATH="https://repo/7.8/"`
)

// TestWithKeyedLineOwnsTheKeyedLine pins WithKeyedLine's edit: the first line
// with the key is replaced in place, later ones are dropped, a missing key is
// appended, every other line (comments included) keeps its place, and a
// second apply changes nothing.
func TestWithKeyedLineOwnsTheKeyedLine(t *testing.T) {
	cases := []struct {
		name, before, want string
		missing            bool
		opts               []FileOption
	}{
		{name: "unquoted legacy replaced in place",
			before: "# profile\nexport PKG_PATH=https://repo/7.8/\nPATH=/bin\n",
			want:   "# profile\n" + pkgQuoted + "\nPATH=/bin\n"},
		{name: "older value and duplicate replaced once",
			before: "export PKG_PATH=\"https://repo/7.7/\"\n# keep\nexport PKG_PATH=x\n",
			want:   pkgQuoted + "\n# keep\n"},
		{name: "already converged",
			before: "a\n" + pkgQuoted + "\nb\n", want: "a\n" + pkgQuoted + "\nb\n"},
		{name: "missing key appended",
			before: "a\n# export PKG_PATH=commented\n", want: "a\n# export PKG_PATH=commented\n" + pkgQuoted + "\n"},
		{name: "missing file created",
			missing: true, want: pkgQuoted + "\n"},
		{name: "removal then keyed then appended line",
			before: "old\nexport PKG_PATH=y\n",
			want:   pkgQuoted + "\nnew\n",
			opts:   []FileOption{WithoutLine("old"), WithLine("new")}},
		{name: "exact repeat is one declaration",
			before: "", want: pkgQuoted + "\n",
			opts: []FileOption{WithKeyedLine(pkgKey, pkgQuoted)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resource.ResetRepository()
			path := filepath.Join(t.TempDir(), "profile")
			if !tc.missing {
				if err := os.WriteFile(path, []byte(tc.before), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			opts := append([]FileOption{WithKeyedLine(pkgKey, pkgQuoted), WithMode(0o644)}, tc.opts...)
			for run := 1; run <= 2; run++ {
				if err := Ensure(path, opts...); err != nil {
					t.Fatalf("run %d: Ensure: %v", run, err)
				}
				got, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if string(got) != tc.want {
					t.Fatalf("run %d: content = %q, want %q", run, got, tc.want)
				}
			}
		})
	}
}

// TestWithKeyedLineRefusesAmbiguousOwnership pins the declaration-time
// conflicts: each would leave two owners for one line or never converge.
func TestWithKeyedLineRefusesAmbiguousOwnership(t *testing.T) {
	cases := []struct {
		name string
		opts []FileOption
		want string
	}{
		{"empty key", []FileOption{WithKeyedLine("", "x")}, "non-empty key"},
		{"line without key", []FileOption{WithKeyedLine("A=", "B=1")}, "must start with its key"},
		{"multi-line", []FileOption{WithKeyedLine("A=", "A=1\nB=2")}, "line break"},
		{"same key twice", []FileOption{WithKeyedLine("A=", "A=1"), WithKeyedLine("A=", "A=2")}, "two different lines"},
		{"prefix keys", []FileOption{WithKeyedLine("A", "A=1"), WithKeyedLine("AB=", "AB=2")}, "overlap"},
		{"WithLine owned by key", []FileOption{WithKeyedLine("A=", "A=1"), WithLine("A=2")}, "already owns it"},
		{"WithoutLine owned by key", []FileOption{WithKeyedLine("A=", "A=1"), WithoutLine("A=0")}, "already owns it"},
		{"with content", []FileOption{WithKeyedLine("A=", "A=1"), WithContent("x")}, "cannot be combined"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resource.ResetRepository()
			path := filepath.Join(t.TempDir(), "f")
			err := Ensure(path, tc.opts...)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to mention %q", err, tc.want)
			}
			if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
				t.Fatalf("refused declaration wrote %s (stat err %v)", path, statErr)
			}
		})
	}
}

// TestKeyedLinesSurvivePlanRoundTrip records a keyed edit, lowers and encodes
// it, decodes it as a destination would and applies it through the plan
// handler: the destination must converge exactly like a direct Ensure.
func TestKeyedLinesSurvivePlanRoundTrip(t *testing.T) {
	resource.ResetRepository()
	path := filepath.Join(t.TempDir(), "profile")
	if err := os.WriteFile(path, []byte("a\nexport PKG_PATH=old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := build(path, WithKeyedLine(pkgKey, pkgQuoted), WithLine("z"), WithMode(0o644))
	if err != nil {
		t.Fatal(err)
	}
	f.resource = resource.Register("File", f.resourceName(), f)
	op, err := planHandler{}.ToOp(f.planDraft())
	if err != nil {
		t.Fatalf("ToOp: %v", err)
	}
	raw, err := plan.EncodeOp(op)
	if err != nil {
		t.Fatalf("EncodeOp: %v", err)
	}
	if !strings.Contains(string(raw), `"keyed_lines":[{"key":"export PKG_PATH=","line":"export PKG_PATH=\"https://repo/7.8/\""}]`) {
		t.Fatalf("encoded op lacks keyed_lines: %s", raw)
	}
	if got := plan.RequiredVersion([]plan.Op{op}); got != plan.VersionKeyedLines {
		t.Fatalf("RequiredVersion = %d, want %d", got, plan.VersionKeyedLines)
	}
	decoded, err := plan.DecodeOp(raw)
	if err != nil {
		t.Fatalf("DecodeOp: %v", err)
	}
	if err := (planHandler{}).Apply(decoded, plan.ApplyContext{}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := "a\n" + pkgQuoted + "\nz\n"; string(got) != want {
		t.Fatalf("content = %q, want %q", got, want)
	}
}

// TestPlanApplyRefusesKeyedLinesWithContent guards hand-authored plans: a
// keyed edit must not combine with whole-file content on the wire either.
func TestPlanApplyRefusesKeyedLinesWithContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f")
	op := plan.Op{Op: plan.KindFile, Path: path, ContentB64: "eA==",
		KeyedLines: []plan.KeyedLine{{Key: "A=", Line: "A=1"}}}
	err := (planHandler{}).Apply(op, plan.ApplyContext{})
	if err == nil || !strings.Contains(err.Error(), "keyed_lines cannot combine") {
		t.Fatalf("err = %v, want the content conflict", err)
	}
}

// summaryOf renders the current apply report the way PrintSummary would.
func summaryOf(t *testing.T) string {
	t.Helper()
	var buf strings.Builder
	resource.PrintSummary(&buf)
	return buf.String()
}

// TestWithKeyedLineDropWarnsLoudlyAndReportsChange pins the fix for the
// data-loss-adjacent bug this task (8c2) closes: a key that is accidentally
// too broad for the file's actual content (an "export " prefix key on a
// /root/.profile-style file, instead of the narrow "export PKG_PATH=" the
// recipe author meant) matches, and therefore drops, every other line
// sharing that prefix — EDITOR, PAGER and HTTP_PROXY here. That must be
// loud: logged at Warn (so it survives -quiet, which only raises the level
// past Info, see internal/logger.Level) and never at Info-and-below, never
// naming the dropped lines' own text, and the resource must be reported
// StatusChanged so it shows up in the apply summary an operator actually
// reads.
func TestWithKeyedLineDropWarnsLoudlyAndReportsChange(t *testing.T) {
	resource.ResetRepository()
	resource.ResetReport()
	path := filepath.Join(t.TempDir(), "profile")
	before := "export EDITOR=vi\nexport PAGER=less\nexport PKG_PATH=old\nexport HTTP_PROXY=proxy.example:8080\n"
	if err := os.WriteFile(path, []byte(before), 0o644); err != nil {
		t.Fatal(err)
	}

	// Capture at LevelWarn, the level -quiet runs at: if the drop report
	// does not survive here, an operator running -quiet never sees it.
	quietOutput := testutil.CaptureLog(t, logger.LevelWarn)

	broadKey := "export "
	newPkgLine := `export PKG_PATH="https://repo/"`
	if err := Ensure(path, WithKeyedLine(broadKey, newPkgLine), WithMode(0o644)); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Confirm the bug is real: EDITOR, PAGER and HTTP_PROXY are gone, only
	// the intended PKG_PATH line remains.
	if want := newPkgLine + "\n"; string(got) != want {
		t.Fatalf("content = %q, want %q (EDITOR/PAGER/HTTP_PROXY must be dropped by the over-broad key)", got, want)
	}

	logged := quietOutput()
	if !strings.Contains(logged, "drops 3") {
		t.Fatalf("quiet-level log = %q, want it to report the 3 dropped lines even under -quiet", logged)
	}
	if !strings.Contains(logged, broadKey) {
		t.Fatalf("quiet-level log = %q, want it to name the key", logged)
	}
	for _, secret := range []string{"EDITOR", "PAGER", "HTTP_PROXY", "vi", "less", "proxy.example"} {
		if strings.Contains(logged, secret) {
			t.Fatalf("quiet-level log = %q, must not repeat dropped line text (%q leaked)", logged, secret)
		}
	}

	if summary := summaryOf(t); !strings.Contains(summary, "changed "+"File["+path+"]") {
		t.Fatalf("summary = %q, want the file reported changed", summary)
	}
}

// TestWithKeyedLineDropDebugLogsRecoverableText confirms the dropped lines'
// text is recoverable at Debug (explicitly sanctioned by task 8c2's review:
// fine for local debugging, never at Warn or Info since the text is not
// necessarily redaction-safe).
func TestWithKeyedLineDropDebugLogsRecoverableText(t *testing.T) {
	resource.ResetRepository()
	resource.ResetReport()
	path := filepath.Join(t.TempDir(), "profile")
	before := "export EDITOR=vi\nexport PKG_PATH=old\n"
	if err := os.WriteFile(path, []byte(before), 0o644); err != nil {
		t.Fatal(err)
	}

	debugOutput := testutil.CaptureLog(t, logger.LevelDebug)
	if err := Ensure(path, WithKeyedLine("export ", `export PKG_PATH="new"`), WithMode(0o644)); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if logged := debugOutput(); !strings.Contains(logged, "PKG_PATH=old") {
		t.Fatalf("debug-level log = %q, want the dropped line's text recoverable at Debug", logged)
	}
}

// TestWithKeyedLineNarrowKeyDoesNotWarn pins the non-regression: a correctly
// narrow key that replaces or appends its one line, with nothing else to
// drop, must not warn (a Warn on every ordinary converge would bury the
// signal this task adds Warn for).
func TestWithKeyedLineNarrowKeyDoesNotWarn(t *testing.T) {
	cases := []struct {
		name, before string
	}{
		{"replaces its one line", "export EDITOR=vi\nexport PKG_PATH=old\n"},
		{"appends, nothing to own yet", "export EDITOR=vi\n"},
		{"already converged", pkgQuoted + "\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resource.ResetRepository()
			resource.ResetReport()
			path := filepath.Join(t.TempDir(), "profile")
			if err := os.WriteFile(path, []byte(tc.before), 0o644); err != nil {
				t.Fatal(err)
			}
			warnOutput := testutil.CaptureLog(t, logger.LevelWarn)
			if err := Ensure(path, WithKeyedLine(pkgKey, pkgQuoted), WithMode(0o644)); err != nil {
				t.Fatalf("Ensure: %v", err)
			}
			if logged := warnOutput(); logged != "" {
				t.Fatalf("warn-level log = %q, want no warning for a narrow key with nothing to drop", logged)
			}
		})
	}
}
