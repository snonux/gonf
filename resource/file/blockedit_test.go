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
	hostsBegin = "# BEGIN GONF fleet"
	hostsEnd   = "# END GONF fleet"
	loopback   = "127.0.0.1 localhost\n"
	toolLine   = "10.9.9.9 tool-owned\n"
)

// TestWithBlockOwnsOnlyItsRegion pins WithBlock's edit: the lines between the
// markers are replaced, everything outside (another tool's lines, comments)
// keeps its place, a file without markers gets the block appended, and a
// second apply changes nothing.
func TestWithBlockOwnsOnlyItsRegion(t *testing.T) {
	block := hostsBegin + "\n10.0.0.1 a\n10.0.0.2 b\n" + hostsEnd + "\n"
	cases := []struct {
		name, before, want string
		missing            bool
		opts               []FileOption
	}{
		{name: "no markers: block appended",
			before: loopback + toolLine, want: loopback + toolLine + block},
		{name: "stale block content replaced, outside untouched",
			before: loopback + hostsBegin + "\n10.0.0.1 a\n10.0.0.3 stale\n" + hostsEnd + "\n" + toolLine,
			want:   loopback + block + toolLine},
		{name: "already converged",
			before: loopback + block + toolLine, want: loopback + block + toolLine},
		{name: "indented markers kept as they are",
			before: "  " + hostsBegin + "\nold\n" + hostsEnd + "  \n",
			want:   "  " + hostsBegin + "\n10.0.0.1 a\n10.0.0.2 b\n" + hostsEnd + "  \n"},
		{name: "missing file created",
			missing: true, want: block},
		{name: "block before other line edits",
			before: loopback + "old\n",
			want:   loopback + block + "new\n",
			opts:   []FileOption{WithoutLine("old"), WithLine("new")}},
		{name: "exact repeat is one declaration",
			before: "", want: block,
			opts: []FileOption{WithBlock("fleet", "10.0.0.1 a", "10.0.0.2 b")}},
		{name: "second block with its own markers",
			before: loopback,
			want:   loopback + block + "# BEGIN GONF other\nx\n# END GONF other\n",
			opts:   []FileOption{WithBlock("other", "x")}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resource.ResetRepository()
			path := filepath.Join(t.TempDir(), "hosts")
			if !tc.missing {
				writeTestFile(t, path, tc.before)
			}
			opts := append([]FileOption{WithBlock("fleet", "10.0.0.1 a", "10.0.0.2 b"), WithMode(0o644)}, tc.opts...)
			for run := 1; run <= 2; run++ {
				if err := Ensure(path, opts...); err != nil {
					t.Fatalf("run %d: Ensure: %v", run, err)
				}
				if got := readTestFile(t, path); got != tc.want {
					t.Fatalf("run %d: content = %q, want %q", run, got, tc.want)
				}
			}
		})
	}
}

// TestWithBlockEmptyBlockKeepsMarkers pins that a block without lines owns an
// empty region: its old lines go, the markers stay.
func TestWithBlockEmptyBlockKeepsMarkers(t *testing.T) {
	resource.ResetRepository()
	path := filepath.Join(t.TempDir(), "hosts")
	writeTestFile(t, path, hostsBegin+"\nold\n"+hostsEnd+"\n"+toolLine)
	if err := Ensure(path, WithBlock("fleet"), WithMode(0o644)); err != nil {
		t.Fatal(err)
	}
	if got, want := readTestFile(t, path), hostsBegin+"\n"+hostsEnd+"\n"+toolLine; got != want {
		t.Fatalf("content = %q, want %q", got, want)
	}
}

// TestWithBlockRefusesMalformedMarkers pins that a file whose markers do not
// delimit exactly one region is refused without writing: gonf cannot tell
// which lines it owns.
func TestWithBlockRefusesMalformedMarkers(t *testing.T) {
	cases := []struct{ name, before, want string }{
		{"begin only", loopback + hostsBegin + "\nx\n", "found 1 BEGIN and 0 END"},
		{"end only", loopback + hostsEnd + "\n", "found 0 BEGIN and 1 END"},
		{"begin twice", hostsBegin + "\n" + hostsBegin + "\n" + hostsEnd + "\n", "found 2 BEGIN and 1 END"},
		{"end before begin", hostsEnd + "\nx\n" + hostsBegin + "\n", "END before BEGIN"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resource.ResetRepository()
			path := filepath.Join(t.TempDir(), "hosts")
			writeTestFile(t, path, tc.before)
			err := Ensure(path, WithBlock("fleet", "a"), WithMode(0o644))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to mention %q", err, tc.want)
			}
			if got := readTestFile(t, path); got != tc.before {
				t.Fatalf("refused apply rewrote the file: %q", got)
			}
		})
	}
}

// TestWithBlockRefusesAmbiguousOwnership pins the declaration-time conflicts:
// each would leave two owners for one line or never converge.
func TestWithBlockRefusesAmbiguousOwnership(t *testing.T) {
	cases := []struct {
		name string
		opts []FileOption
		want string
	}{
		{"empty name", []FileOption{WithBlock("", "a")}, "non-empty name"},
		{"name with line break", []FileOption{WithBlock("a\nb", "a")}, "line break"},
		{"name with whitespace", []FileOption{WithBlock(" a", "x")}, "surrounding whitespace"},
		{"same name twice", []FileOption{WithBlock("a", "x"), WithBlock("a", "y")}, "two different line sets"},
		{"multi-line block line", []FileOption{WithBlock("a", "x\ny")}, "line break"},
		{"block line is its own marker", []FileOption{WithBlock("a", "# END GONF a")}, "is a marker"},
		{"block line is another block's marker", []FileOption{WithBlock("a", "  # BEGIN GONF b"), WithBlock("b")}, "is a marker"},
		{"WithLine owned by block", []FileOption{WithBlock("a", "x"), WithLine("x")}, "owned by WithBlock"},
		{"WithoutLine owned by block", []FileOption{WithBlock("a", "x"), WithoutLine("x")}, "owned by WithBlock"},
		{"WithoutLine removes a marker", []FileOption{WithBlock("a", "x"), WithoutLine("# END GONF a")}, "owned by WithBlock"},
		{"indented WithLine is a marker", []FileOption{WithBlock("a", "x"), WithLine("  # BEGIN GONF a")}, "is a marker"},
		{"keyed key prefixes a block line", []FileOption{WithBlock("a", "  A=1"), WithKeyedLine("A=", "A=2")}, "prefixes line"},
		{"keyed key prefixes a marker", []FileOption{WithBlock("a", "x"), WithKeyedLine("# BEGIN", "# BEGIN x")}, "prefixes line"},
		{"with content", []FileOption{WithBlock("a", "x"), WithContent("x")}, "cannot be combined"},
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

// TestBlocksSurvivePlanRoundTrip records a block, lowers and encodes it,
// decodes it as a destination would and applies it through the plan handler:
// the destination must converge exactly like a direct Ensure.
func TestBlocksSurvivePlanRoundTrip(t *testing.T) {
	resource.ResetRepository()
	path := filepath.Join(t.TempDir(), "hosts")
	writeTestFile(t, path, loopback+hostsBegin+"\nstale\n"+hostsEnd+"\n"+toolLine)
	f, err := build(path, WithBlock("fleet", "10.0.0.1 a"), WithLine("z"), WithMode(0o644))
	if err != nil {
		t.Fatal(err)
	}
	f.resource, _ = resource.Register("File", f.resourceName(), f)
	op, err := planHandler{}.ToOp(f.planDraft())
	if err != nil {
		t.Fatalf("ToOp: %v", err)
	}
	raw, err := plan.EncodeOp(op)
	if err != nil {
		t.Fatalf("EncodeOp: %v", err)
	}
	if !strings.Contains(string(raw), `"blocks":[{"name":"fleet","lines":["10.0.0.1 a"]}]`) {
		t.Fatalf("encoded op lacks blocks: %s", raw)
	}
	if got := plan.RequiredVersion([]plan.Op{op}); got != plan.VersionBlocks {
		t.Fatalf("RequiredVersion = %d, want %d", got, plan.VersionBlocks)
	}
	decoded, err := plan.DecodeOp(raw)
	if err != nil {
		t.Fatalf("DecodeOp: %v", err)
	}
	if err := (planHandler{}).Apply(decoded, plan.ApplyContext{}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	want := loopback + hostsBegin + "\n10.0.0.1 a\n" + hostsEnd + "\n" + toolLine + "z\n"
	if got := readTestFile(t, path); got != want {
		t.Fatalf("content = %q, want %q", got, want)
	}
}

// TestPlanApplyRefusesBlocksWithContent guards hand-authored plans: a block
// must not combine with whole-file content on the wire either.
func TestPlanApplyRefusesBlocksWithContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f")
	op := plan.Op{Op: plan.KindFile, Path: path, Payload: plan.FilePayload{ContentB64: "eA==",
		Blocks: []plan.Block{{Name: "a", Lines: []string{"x"}}}}}
	err := (planHandler{}).Apply(op, plan.ApplyContext{})
	if err == nil || !strings.Contains(err.Error(), "cannot combine with content_b64") {
		t.Fatalf("err = %v, want the content conflict", err)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("refused op wrote %s", path)
	}
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readTestFile(t *testing.T, path string) string {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(got)
}
