package cli

import (
	"os"
	"path/filepath"
	"strings"
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
// secret-bearing plan.jsonl - and, for a WithSensitive payload above
// plan.MaxInlineContent, a plaintext blob under outDir/blobs/ (plan/blob.go,
// Store.openBlobsDir) - into a directory that is itself inside a git
// worktree, where an operator's later `git add -A` / `git commit` could put
// either into history (docs/plan-encryption.md, threat T2). Both paths are
// probed, not just plan.jsonl: a repo that ignores plan.jsonl but not
// blobs/ is the worse case in practice, since plan.jsonl then carries no
// secret bytes at all while blobs/ holds the cleartext, and an operator who
// follows this warning's first suggested fix (ignore plan.jsonl) and sees
// silence on the next run would otherwise commit blobs/ believing it is
// covered. Sealing (task 2b2) does not exist yet, so there is no flag to
// recommend instead of a .gitignore entry or a private -o directory.
//
// This is warn-only and fails safe in every direction: it never refuses or
// delays the plan write (which has already happened by the time this is
// called), and any failure to determine the answer - git missing, a
// timeout, an odd repository state - is treated as "cannot tell" for that
// path and never turns into a false warning for it. Detection is a cheap,
// conservative filesystem walk (costs no process start for the common case
// of a private, non-git -o directory) followed, only once that finds a
// repository, by one bounded git subprocess per candidate path (plan.jsonl
// always, blobs only when outDir/blobs actually exists - nothing warns
// about a blobs/ that was never written) that answers the one question a
// stat-only walk cannot: does gitignore, with all its rule precedence,
// cover this path.
func warnIfPlanUnignoredInGitWorktree(outDir string) {
	if !hasGitAncestor(outDir) {
		return
	}
	paths := []string{"plan.jsonl"}
	if hasBlobDir(outDir) {
		paths = append(paths, "blobs")
	}
	unignored := unignoredPaths(outDir, paths)
	if len(unignored) == 0 {
		return
	}
	eprintf("plan: %s is inside a git worktree and %s %s not gitignored; "+
		"a git add/commit could put this secret-bearing plan into version-control history. "+
		"Add plan.jsonl (and blobs/) to .gitignore, or write the plan to a private -o <dir> "+
		"outside any git checkout (sealing plans is planned but not implemented yet)\n",
		outDir, describeUnignored(unignored), isAre(unignored))
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

// hasBlobDir reports whether outDir/blobs exists as a directory: the
// location a WithSensitive payload above plan.MaxInlineContent is blobbed
// out to during recording (plan/blob.go, Store.openBlobsDir; e.g.
// outDir/blobs/dst-<hash>/secretfile.txt for a blobbed SyncDir member).
// warnIfPlanUnignoredInGitWorktree only probes git-ignore coverage for
// "blobs" when this is true, so a plan with nothing blobbed out (every
// sensitive payload stayed inline) never warns about a blobs/ directory
// that was never written.
func hasBlobDir(outDir string) bool {
	info, err := os.Stat(filepath.Join(outDir, "blobs"))
	return err == nil && info.IsDir()
}

// unignoredPaths runs pathUnignoredByGit once per candidate path (each of
// "plan.jsonl" and, when present, "blobs") and returns the ones git reports
// as NOT covered by dir's ignore rules, in the order given.
func unignoredPaths(dir string, paths []string) []string {
	var unignored []string
	for _, p := range paths {
		if pathUnignoredByGit(dir, p) {
			unignored = append(unignored, p)
		}
	}
	return unignored
}

// pathUnignoredByGit reports whether path ("plan.jsonl" or "blobs") is NOT
// covered by the ignore rules of the git worktree rooted at (or above) dir,
// by running `git check-ignore -q path` with dir as the working directory
// (internal/exec, so it shares the module's process-group signalling and
// output capture, bounded here by gitCheckIgnoreTimeout rather than the
// package's much longer command default). Each path gets its own
// subprocess rather than one call naming both: git 2.x's `-q` refuses more
// than one pathname at a time ("--quiet is only valid with a single
// pathname", exit 128 - confirmed against git 2.55), and checking one at a
// time is also what lets the warning name exactly which path is the
// problem. check-ignore only reads ignore rules (.gitignore files,
// $GIT_DIR/info/exclude, the configured core.excludesFile); it runs no
// hooks and evaluates no arbitrary config. Per git-check-ignore(1): exit 0
// means the path is ignored, exit 1 means it is not, and any other outcome
// - a non-1 non-zero exit (e.g. run outside a git checkout after all, a
// corrupt repository) or a failure to start or finish the command at all
// (git missing, the timeout) - is "cannot tell" and answers false, the
// fail-safe default of "no warning" for that path.
func pathUnignoredByGit(dir, path string) bool {
	_, _, exitCode, err := exec.RunWith(exec.Opts{Dir: dir, Timeout: gitCheckIgnoreTimeout}, "git", "check-ignore", "-q", path)
	if err != nil {
		return false
	}
	return exitCode == 1
}

// describeUnignored renders the unignored path list for the warning text:
// "blobs" is shown as "blobs/" to match the fix advice right after it
// ("Add plan.jsonl (and blobs/) to .gitignore"), joined with "and" - there
// are never more than the two candidate paths from
// warnIfPlanUnignoredInGitWorktree.
func describeUnignored(paths []string) string {
	shown := make([]string, len(paths))
	for i, p := range paths {
		if p == "blobs" {
			p = "blobs/"
		}
		shown[i] = p
	}
	return strings.Join(shown, " and ")
}

// isAre picks the verb form matching how many paths are unignored, so the
// warning reads "plan.jsonl is not gitignored" for one path and "plan.jsonl
// and blobs/ are not gitignored" for both.
func isAre(paths []string) string {
	if len(paths) > 1 {
		return "are"
	}
	return "is"
}
