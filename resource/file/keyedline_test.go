package file

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/snonux/gonf/api/options"
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
