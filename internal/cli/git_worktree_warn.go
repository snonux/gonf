package cli

import (
	"os"
	"path/filepath"
	"time"

	"github.com/snonux/gonf/internal/exec"
)

// gitCheckIgnoreTimeout bounds the `git check-ignore` probe below in
// warnIfPlanUnignoredInGitWorktree, well under internal/exec's process-wide
// default command timeout (5 minutes): a hung or misbehaving git on $PATH (a
// broken credential helper, an unresponsive network filesystem under the
// worktree) must never stall `gonf plan -o`, which has already written
// plan.jsonl to disk by the time this runs and only prints a warning.
const gitCheckIgnoreTimeout = 5 * time.Second

// warnIfPlanUnignoredInGitWorktree is the 0b2 (phase 0 of the w82
// plan-encryption design) warning: gonf plan -o can write a plaintext,
// secret-bearing plan.jsonl into a directory that is itself inside a git
// worktree, where an operator's later `git add -A` / `git commit` could put
// it into history (docs/plan-encryption.md, threat T2). Sealing (task 2b2)
// does not exist yet, so there is no flag to recommend instead of a
// .gitignore entry or a private -o directory.
//
// This is warn-only and fails safe in every direction: it never refuses or
// delays the plan write (which has already happened by the time this is
// called), and any failure to determine the answer - git missing, a
// timeout, an odd repository state - is treated as "cannot tell" and prints
// nothing, never a false warning. Detection is two cheap, conservative
// steps, run only for a plan that already has secret material (the caller,
// warnSensitivePlan, checks that): first a pure filesystem walk that costs
// no process start for the common case of a private, non-git -o directory,
// then, only once that finds a repository, one bounded git subprocess that
// answers the one question a stat-only walk cannot (does gitignore, with
// all its rule precedence, cover this path).
func warnIfPlanUnignoredInGitWorktree(outDir string) {
	if !hasGitAncestor(outDir) {
		return
	}
	if !planFileUnignoredByGit(outDir) {
		return
	}
	eprintf("plan: %s is inside a git worktree and plan.jsonl there is not gitignored; "+
		"a git add/commit could put this secret-bearing plan into version-control history. "+
		"Add plan.jsonl (and blobs/) to .gitignore, or write the plan to a private -o <dir> "+
		"outside any git checkout (sealing plans is planned but not implemented yet)\n",
		outDir)
}

// hasGitAncestor reports whether dir, or an ancestor of it up to the
// filesystem root, holds a ".git" entry: a directory for an ordinary
// checkout, or a "gitdir: ..." file for a linked worktree or a submodule
// (exactly what this repository's own worktrees under .claude/worktrees
// use). It only stats path components with os.Lstat - it never runs git,
// reads .git/config or follows a "gitdir:" file's target - so it cannot
// execute or trust anything the repository itself controls; it is purely a
// cheap pre-filter before the git subprocess below.
func hasGitAncestor(dir string) bool {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return false
	}
	for {
		if _, err := os.Lstat(filepath.Join(abs, ".git")); err == nil {
			return true
		}
		parent := filepath.Dir(abs)
		if parent == abs {
			return false
		}
		abs = parent
	}
}

// planFileUnignoredByGit reports whether "plan.jsonl" is NOT covered by the
// ignore rules of the git worktree rooted at (or above) dir, by running
// `git check-ignore -q plan.jsonl` with dir as the working directory
// (internal/exec, so it shares the module's process-group signalling and
// output capture, bounded here by gitCheckIgnoreTimeout rather than the
// package's much longer command default). check-ignore only reads ignore
// rules (.gitignore files, $GIT_DIR/info/exclude, the configured
// core.excludesFile); it runs no hooks and evaluates no arbitrary config.
// Per git-check-ignore(1): exit 0 means the path is ignored, exit 1 means it
// is not, and any other outcome - a non-1 non-zero exit (e.g. run outside a
// git checkout after all, a corrupt repository) or a failure to start or
// finish the command at all (git missing, the timeout) - is "cannot tell"
// and answers false, the fail-safe default of "no warning".
func planFileUnignoredByGit(dir string) bool {
	_, _, exitCode, err := exec.RunWith(exec.Opts{Dir: dir, Timeout: gitCheckIgnoreTimeout}, "git", "check-ignore", "-q", "plan.jsonl")
	if err != nil {
		return false
	}
	return exitCode == 1
}
