package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/snonux/gonf/plan"
)

// writeTouchPlan writes a plan that touches target into dir/name and returns
// the plan path.
func writeTouchPlan(t *testing.T, dir, name, target string) string {
	t.Helper()
	raw, err := plan.EncodePlan([]plan.Op{
		{Op: plan.KindPlan, Version: plan.CurrentVersion, ID: "read"},
		{Op: plan.KindCommand, Payload: plan.CommandPayload{Bin: "touch", Args: []string{target}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// requireNotTouched fails if the plan's command ran.
func requireNotTouched(t *testing.T, target string) {
	t.Helper()
	if _, err := os.Stat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("refused plan was applied: %v", err)
	}
}

// TestCLIApplyRefusesSymlinkedPlanFile: `gonf apply` reads the plan file with
// plan.ReadPrivateFile, so a plan.jsonl that is a symlink (here to a real,
// valid plan) is refused instead of followed, and nothing is applied.
func TestCLIApplyRefusesSymlinkedPlanFile(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "touched")
	writeTouchPlan(t, root, "real.jsonl", target)
	link := filepath.Join(root, "plan.jsonl")
	if err := os.Symlink("real.jsonl", link); err != nil {
		t.Fatal(err)
	}
	code, stderr := runGonf(t, "apply", link)
	want := "apply: read " + link + ": plan: " + link + " is a symlink; refusing to read a plan through it"
	if code != 1 || !strings.Contains(stderr, want) {
		t.Fatalf("apply of a symlinked plan = %d, %q; want 1 and %q", code, stderr, want)
	}
	requireNotTouched(t, target)
}

// TestCLIApplyRefusesNonRegularPlanFile: a FIFO is refused without blocking on
// it (os.ReadFile would wait for a writer forever), and so is a directory.
func TestCLIApplyRefusesNonRegularPlanFile(t *testing.T) {
	root := t.TempDir()
	fifo := filepath.Join(root, "plan.jsonl")
	if err := unix.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{fifo, root} {
		code, stderr := runGonf(t, "apply", path)
		want := "apply: read " + path + ": plan: " + path + " is not a regular file"
		if code != 1 || !strings.Contains(stderr, want) {
			t.Fatalf("apply of %s = %d, %q; want 1 and %q", path, code, stderr, want)
		}
	}
}

func TestCLIApplyMissingPlanFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plan.jsonl")
	code, stderr := runGonf(t, "apply", path)
	want := "apply: read " + path + ": plan: open " + path + ": no such file or directory"
	if code != 1 || !strings.Contains(stderr, want) {
		t.Fatalf("apply of a missing plan = %d, %q; want 1 and %q", code, stderr, want)
	}
}

// TestCLIApplyRefusesDirectoryPaths: a path that names a directory is refused
// as such and nothing is applied. filepath.Dir/Base would have turned "out/"
// into out/out, so each directory also holds a valid plan named after it,
// which must not be applied either.
func TestCLIApplyRefusesDirectoryPaths(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	target := filepath.Join(root, "touched")
	for _, dir := range []string{"out", filepath.Join("a", "b")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		writeTouchPlan(t, dir, filepath.Base(dir), target)
	}
	for _, path := range []string{"out/", "out/.", ".", "/", "a/b/", "out/.."} {
		code, stderr := runGonf(t, "apply", path)
		want := "apply: read " + path + ": plan: " + path + " does not name a file; a directory is not a plan file"
		if code != 1 || !strings.Contains(stderr, want) {
			t.Fatalf("apply %q = %d, %q; want 1 and %q", path, code, stderr, want)
		}
		requireNotTouched(t, target)
	}
}

// TestCLIApplyFollowsSymlinkedPlanDirectory: only the plan file itself must
// not be a symlink. The directory is reached the normal way, as the elevated
// re-exec of a local run reads its chunk below a $TMPDIR that may be a
// symlinked path (macOS: /var -> /private/var).
func TestCLIApplyFollowsSymlinkedPlanDirectory(t *testing.T) {
	root := t.TempDir()
	realDir := filepath.Join(root, "real")
	if err := os.Mkdir(realDir, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "touched")
	writeTouchPlan(t, realDir, "plan.jsonl", target)
	link := filepath.Join(root, "link")
	if err := os.Symlink("real", link); err != nil {
		t.Fatal(err)
	}
	if code, stderr := runGonf(t, "apply", filepath.Join(link, "plan.jsonl")); code != 0 {
		t.Fatalf("apply through a symlinked directory = %d, %q; want success", code, stderr)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("plan was not applied: %v", err)
	}
}
