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
// pathUnignoredByGit), not a fake, because the whole point of 0b2's design
// is to trust git's own ignore-rule precedence rather than reimplementing
// it. A host without git is exactly the "cannot tell" case the production
// code already fails safe for; a genuinely missing git binary is covered
// separately (without skipping) by
// TestPathUnignoredByGitFailsSafeWithoutGitBinary below, and a nonexistent
// working directory - a different flavour of the same exec-fails-to-start
// fail-safe path - by TestPathUnignoredByGitFailsSafeOnStartFailure, so
// skipping here loses no coverage of gonf's own logic.
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

// The zd2 reproduction: a .gitignore that covers only plan.jsonl (exactly
// the state the 0b2 warning's own suggested fix leaves an operator in), and
// a written blobs/ directory (standing in for a blobbed WithSensitive
// payload, e.g. out/blobs/dst-<hash>/secretfile.txt). Before zd2 this
// stayed silent - a false all-clear, since blobs/ still holds cleartext and
// `git add -An .` would stage it. The warning must now fire and name
// "blobs".
func TestWarnIfPlanUnignoredInGitWorktreeWarnsOnUnignoredBlobs(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "repo")
	initGitDir(t, repo)
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte("plan.jsonl\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	outDir := filepath.Join(repo, "out")
	blobsDir := filepath.Join(outDir, "blobs", "dst-3d12e21e")
	if err := os.MkdirAll(blobsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(blobsDir, "secretfile.txt"), []byte("cleartext secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	out := testutil.CaptureStderr(t, func() { warnIfPlanUnignoredInGitWorktree(outDir) })
	if !strings.Contains(out, "not gitignored") || !strings.Contains(out, "blobs") {
		t.Fatalf("stderr = %q, want a warning naming blobs as unignored (plan.jsonl is ignored, blobs/ is not)", out)
	}
}

// Both plan.jsonl and blobs/ ignored: no warning at all, even though a
// blobs/ directory was written.
func TestWarnIfPlanUnignoredInGitWorktreeBothIgnored(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "repo")
	initGitDir(t, repo)
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte("plan.jsonl\nblobs\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	outDir := filepath.Join(repo, "out")
	blobsDir := filepath.Join(outDir, "blobs", "dst-3d12e21e")
	if err := os.MkdirAll(blobsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	out := testutil.CaptureStderr(t, func() { warnIfPlanUnignoredInGitWorktree(outDir) })
	if out != "" {
		t.Fatalf("stderr = %q, want no warning: both plan.jsonl and blobs/ are gitignored", out)
	}
}

// Neither plan.jsonl nor blobs/ ignored: the warning fires and names both.
func TestWarnIfPlanUnignoredInGitWorktreeBothUnignored(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "repo")
	initGitDir(t, repo)
	outDir := filepath.Join(repo, "out")
	blobsDir := filepath.Join(outDir, "blobs", "dst-3d12e21e")
	if err := os.MkdirAll(blobsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	out := testutil.CaptureStderr(t, func() { warnIfPlanUnignoredInGitWorktree(outDir) })
	if !strings.Contains(out, "plan.jsonl") || !strings.Contains(out, "blobs") ||
		!strings.Contains(out, "and") || !strings.Contains(out, "are not gitignored") {
		t.Fatalf("stderr = %q, want a warning naming both plan.jsonl and blobs/ as unignored", out)
	}
}

// No blobs/ directory was ever written (every sensitive payload stayed
// inline): the warning behaves exactly as 0b2 left it - only plan.jsonl is
// probed, and blobs is never mentioned, wrongly or otherwise.
func TestWarnIfPlanUnignoredInGitWorktreeNoBlobDir(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "repo")
	initGitDir(t, repo)
	outDir := filepath.Join(repo, "out")
	if err := os.MkdirAll(outDir, 0o700); err != nil {
		t.Fatal(err)
	}
	out := testutil.CaptureStderr(t, func() { warnIfPlanUnignoredInGitWorktree(outDir) })
	// The fixed advice sentence always says "(and blobs/)" regardless of
	// what was probed, so the absence check below is on the NAMED-unignored
	// clause, not on the word "blobs" anywhere in the message.
	if !strings.Contains(out, "plan.jsonl is not gitignored") {
		t.Fatalf("stderr = %q, want a warning naming only plan.jsonl (singular \"is\")", out)
	}
	if strings.Contains(out, "are not gitignored") {
		t.Fatalf("stderr = %q must not name blobs/ as unignored (plural \"are\"): no blobs/ directory was ever written", out)
	}
}

// hasBlobDir is true only for an actual directory at outDir/blobs, not for
// a missing path or a plain file that happens to sit there.
func TestHasBlobDir(t *testing.T) {
	outDir := t.TempDir()
	if hasBlobDir(outDir) {
		t.Fatal("hasBlobDir = true before blobs/ exists")
	}
	if err := os.WriteFile(filepath.Join(outDir, "blobs"), []byte("not a dir"), 0o644); err != nil {
		t.Fatal(err)
	}
	if hasBlobDir(outDir) {
		t.Fatal("hasBlobDir = true for a plain file named blobs")
	}
	if err := os.Remove(filepath.Join(outDir, "blobs")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(outDir, "blobs"), 0o700); err != nil {
		t.Fatal(err)
	}
	if !hasBlobDir(outDir) {
		t.Fatal("hasBlobDir = false for an actual blobs/ directory")
	}
}

// A real git worktree that does NOT ignore plan.jsonl: the warning fires,
// names the directory, and mentions .gitignore, -o and -seal (task 2b2,
// now implemented) as the fixes.
func TestWarnIfPlanUnignoredInGitWorktreeWarns(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "repo")
	initGitDir(t, repo)
	outDir := filepath.Join(repo, "out")
	if err := os.MkdirAll(outDir, 0o700); err != nil {
		t.Fatal(err)
	}
	out := testutil.CaptureStderr(t, func() { warnIfPlanUnignoredInGitWorktree(outDir) })
	if !strings.Contains(out, outDir) || !strings.Contains(out, "not gitignored") ||
		!strings.Contains(out, ".gitignore") || !strings.Contains(out, "-o <dir>") ||
		!strings.Contains(out, "-seal") {
		t.Fatalf("stderr = %q, want a warning naming %s, mentioning .gitignore, -o <dir> and -seal", out, outDir)
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

// pathUnignoredByGit fails safe (answers false, never true) when git itself
// cannot answer the question, so a detection problem can never turn into a
// false warning. A nonexistent working directory makes the command fail to
// start, which is exactly the class of failure this must swallow, for
// either candidate path.
func TestPathUnignoredByGitFailsSafeOnStartFailure(t *testing.T) {
	requireGit(t)
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	if pathUnignoredByGit(missing, "plan.jsonl") {
		t.Fatalf("pathUnignoredByGit(%s, plan.jsonl) = true, want false (fail safe: dir does not even exist)", missing)
	}
	if pathUnignoredByGit(missing, "blobs") {
		t.Fatalf("pathUnignoredByGit(%s, blobs) = true, want false (fail safe: dir does not even exist)", missing)
	}
}

// pathUnignoredByGit also fails safe when "git" itself cannot be found on
// $PATH at all - a different flavour of "exec fails to start" than
// TestPathUnignoredByGitFailsSafeOnStartFailure's nonexistent working
// directory. This test does not call requireGit: the whole point is to run
// with git genuinely unresolvable via exec.LookPath/exec.Command, which
// t.Setenv("PATH", "") guarantees regardless of whether a real git binary
// exists on this host. t.Setenv panics if an ancestor test called
// t.Parallel; this file has none, matching AGENTS.md's "Test seams"
// convention for tests that mutate process-wide state.
func TestPathUnignoredByGitFailsSafeWithoutGitBinary(t *testing.T) {
	t.Setenv("PATH", "")
	dir := t.TempDir()
	if pathUnignoredByGit(dir, "plan.jsonl") {
		t.Fatalf("pathUnignoredByGit(%s, plan.jsonl) = true, want false (fail safe: git binary not found on PATH)", dir)
	}
}
