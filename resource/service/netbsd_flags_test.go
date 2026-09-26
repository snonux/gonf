package service

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/snonux/gonf/resource"
)

// requireShellSyntax fails t unless sh(1) accepts content (sh -n). It
// skips when no sh is installed.
func requireShellSyntax(t *testing.T, content string) {
	t.Helper()
	if err := shellSyntax(t, content); err != nil {
		t.Fatalf("sh -n rejects the result: %v\n%s", err, content)
	}
}

// shellSyntax runs sh -n over content and returns its failure.
func shellSyntax(t *testing.T, content string) error {
	t.Helper()
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh(1) not installed; cannot check rc.conf syntax")
	}
	path := filepath.Join(t.TempDir(), "rc.conf")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(sh, "-n", path).CombinedOutput()
	if err != nil {
		return errors.New(strings.TrimSpace(string(out)))
	}
	return nil
}

// TestShellSyntaxCatchesOrphanedContinuation proves requireShellSyntax has
// teeth: the rc.conf the old line-by-line rewrite left behind (task 7b), a
// replaced first line and an orphaned closing-quote line, fails sh -n.
func TestShellSyntaxCatchesOrphanedContinuation(t *testing.T) {
	if err := shellSyntax(t, "nsd_flags='-x'\n  -b\"\n"); err == nil {
		t.Fatal("sh -n must reject an orphaned continuation line")
	}
}

// TestNetBSDFlagsMultiLine pins WithFlags over assignments that span
// several physical lines (task 7b): the value is read across the lines
// (so an equal one changes nothing), a rewrite replaces every line of the
// assignment, and a NAME_flags= line inside another variable's quoted value
// or a quote in a comment is not mistaken for an assignment. Every result
// must still be valid sh.
func TestNetBSDFlagsMultiLine(t *testing.T) {
	for _, tt := range []struct {
		name, rcConf, flags, want string
	}{
		{"double-quoted continuation replaced whole",
			"a=1\nnsd_flags=\"-a \\\n  -b\"\nb=2\n", "-x", "a=1\nnsd_flags='-x'\nb=2\n"},
		{"single-quoted newline replaced whole",
			"nsd_flags='-a\n-b'\nb=2\n", "-x", "nsd_flags='-x'\nb=2\n"},
		{"unquoted continuation replaced whole",
			"nsd_flags=-a\\\n-b # x\nb=2\n", "-x", "nsd_flags='-x'\nb=2\n"},
		{"later multi-line assignment dropped whole",
			"nsd_flags=-4\nb=2\nnsd_flags=\"-a\n-b\"\nc=3\n", "-x", "nsd_flags='-x'\nb=2\nc=3\n"},
		{"double-quoted continuation matches",
			"nsd_flags=\"-a \\\n-b\"\n", "-a -b", "nsd_flags=\"-a \\\n-b\"\n"},
		{"single-quoted newline matches",
			"nsd_flags='-a\n-b'\n", "-a\n-b", "nsd_flags='-a\n-b'\n"},
		{"unquoted continuation matches",
			"nsd_flags=-a\\\nb\n", "-ab", "nsd_flags=-a\\\nb\n"},
		{"assignment inside another quoted value is not one",
			"motd='hi\nnsd_flags=-9\n'\nnsd_flags=-4\n", "-x", "motd='hi\nnsd_flags=-9\n'\nnsd_flags='-x'\n"},
		{"assignment inside another quoted value does not match",
			"motd='hi\nnsd_flags=-9\n'\nnsd_flags=-4\n", "-4", "motd='hi\nnsd_flags=-9\n'\nnsd_flags=-4\n"},
		{"apostrophe in a comment opens no quote",
			"# don't touch\nnsd_flags=-4 # it's set\nb=2\n", "-x", "# don't touch\nnsd_flags='-x'\nb=2\n"},
		{"hash inside a word is no comment",
			"motd=a#'\nnsd_flags=-4\n'\nnsd_flags=-6\n", "-x", "motd=a#'\nnsd_flags=-4\n'\nnsd_flags='-x'\n"},
		{"continuation at end of file",
			"b=2\nnsd_flags=-4\\\n", "-x", "b=2\nnsd_flags='-x'\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			resource.ResetReport()
			b := netbsdFlagsBackend(t, tt.rcConf, "", "")
			s := withFlagsSvc(Service{name: "nsd"}, tt.flags)
			if err := s.applyWith(b); err != nil {
				t.Fatalf("applyWith: %v", err)
			}
			got, err := os.ReadFile(b.rcConf)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tt.want {
				t.Fatalf("rc.conf = %q, want %q", got, tt.want)
			}
			requireShellSyntax(t, string(got))
		})
	}
}

// TestNetBSDFlagsUnterminatedQuoteRefused pins the refusal of an rc.conf
// (or rc.conf.d override, or defaults) whose quote never closes: probe and
// write both fail with the file and line named, and rc.conf is not touched.
func TestNetBSDFlagsUnterminatedQuoteRefused(t *testing.T) {
	const broken = "a=1\nb=2\nnsd_flags=\"-a \\\n-b\n"
	for _, tt := range []struct {
		name, rcConf, defaults, override string
		file                             func(netbsdBackend) string
	}{
		{"rc.conf", broken, "", "", func(b netbsdBackend) string { return b.rcConf }},
		{"override", "nsd_flags=-4\n", "", broken, func(b netbsdBackend) string { return filepath.Join(b.rcConfD, "nsd") }},
		{"defaults", "", broken, "", func(b netbsdBackend) string { return b.rcConfDefaults }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			resource.ResetReport()
			b := netbsdFlagsBackend(t, tt.rcConf, tt.defaults, tt.override)
			s := withFlagsSvc(Service{name: "nsd"}, "-x")
			err := s.applyWith(b)
			if !errors.Is(err, errUnterminatedQuote) {
				t.Fatalf("applyWith err = %v, want errUnterminatedQuote", err)
			}
			if msg := err.Error(); !strings.Contains(msg, tt.file(b)) || !strings.Contains(msg, "line 3") {
				t.Fatalf("error %q must name %s and line 3", msg, tt.file(b))
			}
			if got, _ := os.ReadFile(b.rcConf); string(got) != tt.rcConf {
				t.Fatalf("rc.conf changed to %q", got)
			}
		})
	}
	// setFlags refuses on its own too, not only behind the flagsMatch probe.
	b := netbsdFlagsBackend(t, broken, "", "")
	if err := b.setFlags(unit{name: "nsd"}, "-x"); !errors.Is(err, errUnterminatedQuote) || !strings.Contains(err.Error(), b.rcConf) {
		t.Fatalf("setFlags err = %v, want errUnterminatedQuote naming %s", err, b.rcConf)
	}
	if got, _ := os.ReadFile(b.rcConf); string(got) != broken {
		t.Fatalf("rc.conf changed to %q", got)
	}
}

// TestSplitRcStatements pins the logical-line grouping directly: the line
// ranges of each statement, and the unterminated quote kinds refused.
func TestSplitRcStatements(t *testing.T) {
	lines := rcLines("a=1\nb=\"x\ny\"\n# it's\nc=d\\\ne\nf='g\n\nh' # '\ni=\"\\\"\"\n")
	got, err := splitRcStatements(lines)
	if err != nil {
		t.Fatal(err)
	}
	want := [][2]int{{0, 1}, {1, 3}, {3, 4}, {4, 6}, {6, 9}, {9, 10}}
	if len(got) != len(want) {
		t.Fatalf("got %d statements %+v, want %d", len(got), got, len(want))
	}
	for i, st := range got {
		if st.first != want[i][0] || st.end != want[i][1] {
			t.Errorf("statement %d = [%d,%d), want [%d,%d)", i, st.first, st.end, want[i][0], want[i][1])
		}
		if st.text != strings.Join(lines[st.first:st.end], "\n") {
			t.Errorf("statement %d text = %q", i, st.text)
		}
	}
	for _, content := range []string{"a='x\n", "a=\"x\n", "a=1\nb=\"\\\"\n", "a='x' 'y\n"} {
		if _, err := splitRcStatements(rcLines(content)); !errors.Is(err, errUnterminatedQuote) {
			t.Errorf("splitRcStatements(%q) err = %v, want errUnterminatedQuote", content, err)
		}
	}
}
