package cli

import (
	"os"
	goexec "os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/snonux/gonf/internal/testutil"
)

// requireGit skips the test when no git binary is on $PATH: these tests
// exercise the real `git check-ignore` subprocess (internal/exec, via
// planFileUnignoredByGit), not a fake, because the whole point of 0b2's
// design is to trust git's own ignore-rule precedence rather than
// reimplementing it. A host without git is exactly the "cannot tell" case
// the production code already fails safe for; a genuinely missing git
// binary is covered separately (without skipping) by
// TestPlanFileUnignoredByGitFailsSafeWithoutGitBinary below, and a
// nonexistent working directory - a different flavour of the same
// exec-fails-to-start fail-safe path - by
// TestPlanFileUnignoredByGitFailsSafeOnStartFailure, so skipping here loses
// no coverage of gonf's own logic.
func requireGit(t *testing.T) {
	t.Helper()
	if _, err := goexec.LookPath("git"); err != nil {
		t.Skip("git not found on PATH")
	}
}

// initGitDir creates dir and runs a plain `git init -q` in it.
func initGitDir(t *testing.T, dir string) {
	t.Helper()
	requireGit(t)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := goexec.Command("git", "init", "-q", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init %s: %v\n%s", dir, err, out)
	}
}

// hasGitAncestor finds a ".git" directory at the worktree root and again in
// a subdirectory several levels below it, purely by stat - no git process
// runs for this check, so it needs no git binary.
func TestHasGitAncestorFindsRootAndNestedDir(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(root, "a", "b", "c")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if !hasGitAncestor(root) {
		t.Fatal("hasGitAncestor(root) = false, want true")
	}
	if !hasGitAncestor(nested) {
		t.Fatal("hasGitAncestor(nested) = false, want true (walk up to root's .git)")
	}
}

// hasGitAncestor also recognises a linked worktree's ".git" FILE (as this
// module's own .claude/worktrees checkouts have), not only a directory.
func TestHasGitAncestorFindsGitFile(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".git"), []byte("gitdir: /elsewhere\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !hasGitAncestor(root) {
		t.Fatal("hasGitAncestor(root) = false, want true for a \"gitdir:\" .git file")
	}
}

// A directory with no ".git" anywhere above it (up to t.TempDir()'s root,
// which is not itself inside a git checkout) reports false, and reports it
// without running git at all: no PATH lookup, no subprocess.
func TestHasGitAncestorFalseWithoutGit(t *testing.T) {
	dir := t.TempDir()
	if hasGitAncestor(dir) {
		t.Fatalf("hasGitAncestor(%s) = true, want false (no .git anywhere above a fresh temp dir)", dir)
	}
}

// plan.jsonl already listed in the worktree's .gitignore: no warning.
func TestWarnIfPlanUnignoredInGitWorktreeAlreadyIgnored(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "repo")
	initGitDir(t, repo)
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte("plan.jsonl\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	outDir := filepath.Join(repo, "out")
	if err := os.MkdirAll(outDir, 0o700); err != nil {
		t.Fatal(err)
	}
	out := testutil.CaptureStderr(t, func() { warnIfPlanUnignoredInGitWorktree(outDir) })
	if out != "" {
		t.Fatalf("stderr = %q, want no warning: plan.jsonl is already gitignored", out)
	}
}

// A real git worktree that does NOT ignore plan.jsonl: the warning fires,
// names the directory, and mentions .gitignore and -o as the fixes (and
// never claims -seal exists, since 2b2 has not landed).
func TestWarnIfPlanUnignoredInGitWorktreeWarns(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "repo")
	initGitDir(t, repo)
	outDir := filepath.Join(repo, "out")
	if err := os.MkdirAll(outDir, 0o700); err != nil {
		t.Fatal(err)
	}
	out := testutil.CaptureStderr(t, func() { warnIfPlanUnignoredInGitWorktree(outDir) })
	if !strings.Contains(out, outDir) || !strings.Contains(out, "not gitignored") ||
		!strings.Contains(out, ".gitignore") || !strings.Contains(out, "-o <dir>") {
		t.Fatalf("stderr = %q, want a warning naming %s, mentioning .gitignore and -o <dir>", out, outDir)
	}
	if strings.Contains(out, "-seal") && !strings.Contains(out, "not implemented") {
		t.Fatalf("stderr = %q must not claim -seal exists (task 2b2 has not landed)", out)
	}
}

// A target directory with no .git ancestor at all (an ordinary private -o
// dir, not every -o target is a git repo): no warning, and importantly no
// error/panic either - the whole point of the ancestor pre-filter is to
// answer this case without ever starting git.
func TestWarnIfPlanUnignoredInGitWorktreeNoGitAncestor(t *testing.T) {
	outDir := t.TempDir()
	out := testutil.CaptureStderr(t, func() { warnIfPlanUnignoredInGitWorktree(outDir) })
	if out != "" {
		t.Fatalf("stderr = %q, want no warning: outDir has no .git ancestor", out)
	}
}

// planFileUnignoredByGit fails safe (answers false, never true) when git
// itself cannot answer the question, so a detection problem can never turn
// into a false warning. A nonexistent working directory makes the command
// fail to start, which is exactly the class of failure this must swallow.
func TestPlanFileUnignoredByGitFailsSafeOnStartFailure(t *testing.T) {
	requireGit(t)
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	if planFileUnignoredByGit(missing) {
		t.Fatalf("planFileUnignoredByGit(%s) = true, want false (fail safe: dir does not even exist)", missing)
	}
}

// planFileUnignoredByGit also fails safe when "git" itself cannot be found
// on $PATH at all - a different flavour of "exec fails to start" than
// TestPlanFileUnignoredByGitFailsSafeOnStartFailure's nonexistent working
// directory. This test does not call requireGit: the whole point is to run
// with git genuinely unresolvable via exec.LookPath/exec.Command, which
// t.Setenv("PATH", "") guarantees regardless of whether a real git binary
// exists on this host. t.Setenv panics if an ancestor test called
// t.Parallel; this file has none, matching AGENTS.md's "Test seams"
// convention for tests that mutate process-wide state.
func TestPlanFileUnignoredByGitFailsSafeWithoutGitBinary(t *testing.T) {
	t.Setenv("PATH", "")
	dir := t.TempDir()
	if planFileUnignoredByGit(dir) {
		t.Fatalf("planFileUnignoredByGit(%s) = true, want false (fail safe: git binary not found on PATH)", dir)
	}
}
