package api

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/internal/logger"
	"github.com/snonux/gonf/internal/privilege"
	"github.com/snonux/gonf/internal/testutil"
	"github.com/snonux/gonf/plan"
)

// These tests pin RecordPlan's staging contract (api/stage.go): the plan
// directory of `gonf plan -o dir` is written only when the WHOLE record
// succeeds. Blob names are deterministic, so before staging a refused record
// re-packaged blobs into the directory of an earlier good plan and silently
// changed what that plan's plan.jsonl applied.

// stagedSources is the source material of one packaged plan: a glob-synced
// directory (flat blob), a source tree with an empty directory and a symlink
// (tree blob), and a file over plan.MaxInlineContent (single-file blob).
type stagedSources struct {
	glob    string // file inside the glob source dir that tests edit
	globDir string
	tree    string
	big     string
	dst     string // destination root the plan applies into
}

func newStagedSources(t *testing.T) stagedSources {
	t.Helper()
	root := t.TempDir()
	s := stagedSources{
		globDir: filepath.Join(root, "glob-src"),
		tree:    filepath.Join(root, "tree-src"),
		big:     filepath.Join(root, "big-src"),
		dst:     filepath.Join(root, "dst"),
	}
	s.glob = filepath.Join(s.globDir, "f1")
	for _, dir := range []string{s.globDir, filepath.Join(s.tree, "empty")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite(t, s.glob, []byte("glob version one\n"))
	mustWrite(t, filepath.Join(s.tree, "file"), []byte("tree file\n"))
	if err := os.Symlink("file", filepath.Join(s.tree, "link")); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, s.big, bytes.Repeat([]byte("B"), plan.MaxInlineContent+1))
	return s
}

func mustWrite(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// register registers task name recording all three blob kinds, then bad (when
// not nil) - the part that makes the record fail after the blobs were packaged.
func (s stagedSources) register(name string, bad func()) {
	Task(name, "", func() {
		SyncDir(filepath.Join(s.dst, "synced"), filepath.Join(s.globDir, "*"))
		Dir(filepath.Join(s.dst, "tree"), options.WithSource(s.tree))
		InstallFile(filepath.Join(s.dst, "big"), s.big)
		if bad != nil {
			bad()
		}
	})
}

// TestRecordPlanStagesBlobsIntoPlanDir pins the success path: every blob kind
// lands in planDir exactly as a direct write would have produced it (file
// content, empty directory, symlink), the returned ops reference blobs that
// exist there, the recorded plan applies, blobs of an earlier plan that this
// plan does not reference are kept, and no staging directory is left behind.
func TestRecordPlanStagesBlobsIntoPlanDir(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)
	src := newStagedSources(t)
	planDir := filepath.Join(t.TempDir(), "out")
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	src.register("staged_ok", nil)

	stale := filepath.Join(planDir, "blobs", "from-an-earlier-plan")
	if err := os.MkdirAll(stale, 0o700); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(stale, "keep"), []byte("earlier"))

	ops, err := RecordPlan("staged", planDir, "staged_ok")
	if err != nil {
		t.Fatalf("RecordPlan: %v", err)
	}
	assertCommittedBlobs(t, planDir, blobRefsByBase(t, ops))
	if got := mustRead(t, filepath.Join(stale, "keep")); got != "earlier" {
		t.Fatalf("blob of an earlier plan = %q, must be kept", got)
	}
	assertNoStagingLeft(t, tmp)

	// The plan applies from the committed blobs (proves refs and layout).
	if err := ApplyChunks(ops, planDir, privilege.Sudo); err != nil {
		t.Fatalf("ApplyChunks: %v", err)
	}
	if got := mustRead(t, filepath.Join(src.dst, "synced", "f1")); got != "glob version one\n" {
		t.Fatalf("applied glob content = %q", got)
	}
	if got := mustRead(t, filepath.Join(src.dst, "tree", "file")); got != "tree file\n" {
		t.Fatalf("applied tree content = %q", got)
	}
}

// blobRefsByBase maps the destination basename of each staged source ("synced",
// "tree", "big") to the blob ref the recorded ops carry for it. Blob refs are
// "blobs/<destination basename>-<hash>".
func blobRefsByBase(t *testing.T, ops []plan.Op) map[string]string {
	t.Helper()
	blobs := map[string]string{}
	for _, op := range ops {
		for _, base := range []string{"synced", "tree", "big"} {
			if strings.HasPrefix(op.Blob, "blobs/"+base+"-") {
				blobs[base] = op.Blob
			}
		}
	}
	if len(blobs) != 3 {
		t.Fatalf("blob refs = %v, want glob, tree and big-file blobs", blobs)
	}
	return blobs
}

// assertCommittedBlobs checks the three blob kinds in planDir: the glob blob's
// file, the tree blob's file, empty directory and raw symlink target, and the
// big single-file blob's size.
func assertCommittedBlobs(t *testing.T, planDir string, blobs map[string]string) {
	t.Helper()
	at := func(ref string, rel ...string) string {
		return filepath.Join(append([]string{planDir, filepath.FromSlash(ref)}, rel...)...)
	}
	if got := mustRead(t, at(blobs["synced"], "f1")); got != "glob version one\n" {
		t.Fatalf("glob blob content = %q", got)
	}
	treeRef := blobs["tree"]
	if got := mustRead(t, at(treeRef, "file")); got != "tree file\n" {
		t.Fatalf("tree blob file = %q", got)
	}
	if fi, err := os.Stat(at(treeRef, "empty")); err != nil || !fi.IsDir() {
		t.Fatalf("empty directory of the tree was not preserved: %v", err)
	}
	if target, err := os.Readlink(at(treeRef, "link")); err != nil || target != "file" {
		t.Fatalf("symlink of the tree = %q, %v; want raw target %q", target, err, "file")
	}
	if got := mustRead(t, at(blobs["big"])); len(got) != plan.MaxInlineContent+1 {
		t.Fatalf("big file blob has %d bytes, want %d", len(got), plan.MaxInlineContent+1)
	}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// assertNoStagingLeft fails when the temp dir still holds anything: staging
// (and Run's temp dirs) must clean up on success and on failure alike.
func assertNoStagingLeft(t *testing.T, tmp string) {
	t.Helper()
	entries, err := os.ReadDir(tmp)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Fatalf("temp dir %s still holds %v", tmp, names)
	}
}

// refusedRecordCause is one way a record can fail AFTER the blobs of the
// earlier drafts were packaged.
type refusedRecordCause struct {
	name string
	bad  func()
	want string
}

func refusedRecordCauses() []refusedRecordCause {
	const later = "Command[later]"
	return []refusedRecordCause{
		{"dangling dependency", func() {
			Command("true", nil, options.DependsOn(unregisteredDep("File[/typo]")))
		}, "dangling dependency"},
		{"dangling watch", func() {
			Command("true", nil, options.WatchChanges("File[/typo-watch]"))
		}, "dangling watch"},
		{"dependency on a later privilege chunk", func() {
			Command("true", nil, options.WithName("first"), options.DependsOn(unregisteredDep(later)))
			Command("true", nil, options.WithName("later"), options.WithElevate)
		}, "later chunk"},
		{"change watch across privilege chunks", func() {
			unit := Command("true", nil, options.WithName("unit"), options.WithElevate)
			Command("true", nil, options.WithName("gated"), options.OnChange(unit))
		}, "same chunk"},
		{"packaging error of a later resource", func() {
			InstallFile(filepath.Join(os.TempDir(), "gonf-stage-missing-dst"), "/nonexistent/gonf/stage/source")
		}, "package file"},
	}
}

// TestRecordPlanRefusalLeavesPlanDirUntouched is the o62 review regression:
// whatever makes RecordPlan fail (the dependency and change-gate pre-flight,
// or an unrelated late packaging error), the plan directory must be exactly as
// it was - byte for byte and mtime for mtime - and a directory that did not
// exist must not be created. It covers both an untouched-absent directory and
// the destructive case: a good plan already recorded there whose source file
// was edited before the refused run re-packaged the same deterministic blob
// name; the old plan.jsonl must keep applying the content it was recorded with.
func TestRecordPlanRefusalLeavesPlanDirUntouched(t *testing.T) {
	for _, tc := range refusedRecordCauses() {
		t.Run(tc.name, func(t *testing.T) {
			ResetForTest()
			t.Cleanup(ResetForTest)
			src := newStagedSources(t)
			tmp := t.TempDir()
			t.Setenv("TMPDIR", tmp)

			requireRefusalLeavesAbsentDirAbsent(t, src, tc)
			requireRefusalKeepsEarlierPlan(t, src, tc)
			assertNoStagingLeft(t, tmp)
		})
	}
}

// requireRefusalLeavesAbsentDirAbsent: a refused record into a directory that
// does not exist must not create it.
func requireRefusalLeavesAbsentDirAbsent(t *testing.T, src stagedSources, tc refusedRecordCause) {
	t.Helper()
	absent := filepath.Join(t.TempDir(), "not-yet")
	src.register("refused_absent", tc.bad)
	_, err := RecordPlan("refused", absent, "refused_absent")
	if err == nil || !strings.Contains(err.Error(), tc.want) {
		t.Fatalf("RecordPlan error = %v, want it to contain %q", err, tc.want)
	}
	testutil.RequireUnchanged(t, testutil.DirSnapshot{Absent: true}, absent)
}

// requireRefusalKeepsEarlierPlan records a good plan (plan.jsonl included),
// edits the source, and checks that a refused re-record into the same
// directory leaves it byte for byte as it was and that the earlier plan still
// applies the content it was recorded with.
func requireRefusalKeepsEarlierPlan(t *testing.T, src stagedSources, tc refusedRecordCause) {
	t.Helper()
	ResetForTest()
	planDir := filepath.Join(t.TempDir(), "o3")
	src.register("good", nil)
	goodOps, err := RecordPlan("good", planDir, "good")
	if err != nil {
		t.Fatalf("recording the good plan: %v", err)
	}
	raw, err := plan.EncodePlan(goodOps)
	if err != nil {
		t.Fatal(err)
	}
	if err := plan.WritePrivateFile(planDir, "plan.jsonl", raw); err != nil {
		t.Fatal(err)
	}
	before := testutil.Snapshot(t, planDir)

	mustWrite(t, src.glob, []byte("glob version TWO\n")) // edit the source between runs
	ResetForTest()
	src.register("refused_existing", tc.bad)
	if _, err := RecordPlan("refused", planDir, "refused_existing"); err == nil || !strings.Contains(err.Error(), tc.want) {
		t.Fatalf("second RecordPlan error = %v, want it to contain %q", err, tc.want)
	}
	testutil.RequireUnchanged(t, before, planDir)

	if err := ApplyChunks(goodOps, planDir, privilege.Sudo); err != nil {
		t.Fatalf("applying the earlier plan: %v", err)
	}
	if got := mustRead(t, filepath.Join(src.dst, "synced", "f1")); got != "glob version one\n" {
		t.Fatalf("earlier plan applied %q, want the content it was recorded with", got)
	}
}

// TestRecordPlanRefusalWritesNothingWithoutBlobs covers the blob-less refusal
// the reviewer reproduced as an empty leftover directory: no task packages a
// blob, yet the plan dir must still not be created.
func TestRecordPlanRefusalWritesNothingWithoutBlobs(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)
	Task("no_blobs", "", func() {
		Command("true", nil, options.DependsOn(unregisteredDep("File[/typo]")))
	})
	planDir := filepath.Join(t.TempDir(), "o1")
	if _, err := RecordPlan("x", planDir, "no_blobs"); err == nil {
		t.Fatal("want a dangling-dependency refusal")
	}
	testutil.RequireUnchanged(t, testutil.DirSnapshot{Absent: true}, planDir)
}

// TestRunRecordsWithoutStagingAndCleansTemp pins Run's own storage: it records
// straight into its private temp dir and removes it on success and on refusal.
func TestRunRecordsWithoutStagingAndCleansTemp(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)
	src := newStagedSources(t)
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	src.register("run_ok", nil)
	old := elevatedApplyRunner
	t.Cleanup(func() { elevatedApplyRunner = old })
	elevatedApplyRunner = func(_ context.Context, _ privilege.Mode, ch []plan.Op, dir string) error {
		return ApplyPlan(ch, dir)
	}
	if err := Run("run_ok"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := mustRead(t, filepath.Join(src.dst, "tree", "file")); got != "tree file\n" {
		t.Fatalf("applied tree content = %q", got)
	}
	assertNoStagingLeft(t, tmp)

	ResetForTest()
	src.register("run_refused", refusedRecordCauses()[0].bad)
	if err := Run("run_refused"); err == nil {
		t.Fatal("want a refusal")
	}
	assertNoStagingLeft(t, tmp)
}

// TestRecordPlanBlobLessNeverTouchesTempDir pins the lazy staging directory:
// a plan that packages no blob must not need $TMPDIR at all, exactly as before
// staging existed. With TMPDIR pointing at nothing, eager staging failed
// `gonf plan -o dir` for every blob-less task.
func TestRecordPlanBlobLessNeverTouchesTempDir(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "does-not-exist"))
	Task("blob_less", "", func() {
		File(filepath.Join(t.TempDir(), "x"), options.WithContent("inline"))
	})
	planDir := filepath.Join(t.TempDir(), "out")
	ops, err := RecordPlan("blobless", planDir, "blob_less")
	if err != nil {
		t.Fatalf("RecordPlan must not need $TMPDIR without blobs: %v", err)
	}
	if len(ops) < 2 {
		t.Fatalf("ops = %v, want header plus one resource", ops)
	}
	if fi, err := os.Stat(planDir); err != nil || !fi.IsDir() || fi.Mode().Perm() != 0o700 {
		t.Fatalf("plan dir after a successful record: %v, %v; want an owner-only directory", fi, err)
	}
}

// TestRecordPlanWithBlobsStillNeedsTempDir is the negative twin: a plan that
// does package a blob has to stage, so an unusable $TMPDIR is reported as a
// staging error - and planDir stays untouched.
func TestRecordPlanWithBlobsStillNeedsTempDir(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)
	src := newStagedSources(t)
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "does-not-exist"))
	src.register("needs_stage", nil)
	planDir := filepath.Join(t.TempDir(), "out")
	_, err := RecordPlan("x", planDir, "needs_stage")
	if err == nil || !strings.Contains(err.Error(), "staging dir") {
		t.Fatalf("RecordPlan error = %v, want a staging dir error", err)
	}
	testutil.RequireUnchanged(t, testutil.DirSnapshot{Absent: true}, planDir)
}

const (
	stageFatalEnv    = "GONF_STAGE_FATAL_HELPER" // plan dir; also marks the child
	stageFatalBigEnv = "GONF_STAGE_FATAL_BIG"    // large source file to package
)

// TestStageFatalHelperProcess is the child of TestRecordPlanFatalRemovesStaging,
// not a test of its own: it packages a large blob into the staging directory
// and then hits a fail-fast logger.Fatal inside the task body (os.Exit, no
// deferred cleanup).
func TestStageFatalHelperProcess(t *testing.T) {
	planDir := os.Getenv(stageFatalEnv)
	if planDir == "" {
		t.Skip("helper process only")
	}
	big := os.Getenv(stageFatalBigEnv)
	Task("fatal_after_blob", "", func() {
		InstallFile(filepath.Join(filepath.Dir(big), "dst-big"), big)
		logger.Fatal("fail-fast DSL misuse after packaging a blob")
	})
	_, _ = RecordPlan("x", planDir, "fatal_after_blob")
	t.Fatal("logger.Fatal returned")
}

// TestRecordPlanFatalRemovesStaging reproduces the reviewer's leak: a task body
// that hits logger.Fatal after a large InstallFile was packaged used to leave
// $TMPDIR/gonf-plan-stage-*/blobs/... behind, a full copy of the source
// (possibly a rendered secret). The staging directory is now removed by the
// logger's fatal hook, and planDir is not created either.
func TestRecordPlanFatalRemovesStaging(t *testing.T) {
	tmp := t.TempDir() // the child's $TMPDIR: must end up empty
	src := newStagedSources(t)
	planDir := filepath.Join(t.TempDir(), "out")
	cmd := exec.Command(os.Args[0], "-test.run=^TestStageFatalHelperProcess$")
	cmd.Env = append(os.Environ(), stageFatalEnv+"="+planDir, stageFatalBigEnv+"="+src.big, "TMPDIR="+tmp)
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 {
		t.Fatalf("helper: %v, want exit status 1 from logger.Fatal; output:\n%s", err, out)
	}
	if !strings.Contains(string(out), "fail-fast DSL misuse") {
		t.Fatalf("helper did not reach logger.Fatal; output:\n%s", out)
	}
	assertNoStagingLeft(t, tmp)
	testutil.RequireUnchanged(t, testutil.DirSnapshot{Absent: true}, planDir)
}

// TestRecordPlanCommitFailureIsReportedAndPartial pins the honest half of the
// contract: a failure while COMMITTING blobs is reported, returns no ops, and
// may leave the blob store partially updated. The second blob's destination is
// a non-empty directory, so the atomic file replace fails after the first blob
// (already validated and staged) was copied.
func TestRecordPlanCommitFailureIsReportedAndPartial(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)
	src := newStagedSources(t)
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	src.register("commit_fail", nil)
	glob, big := probeGlobAndBigRefs(t)

	planDir := filepath.Join(t.TempDir(), "out")
	blocker := filepath.Join(planDir, filepath.FromSlash(big), "keep")
	if err := os.MkdirAll(filepath.Dir(blocker), 0o700); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, blocker, []byte("blocks the replace"))

	ResetForTest()
	src.register("commit_fail", nil)
	ops, err := RecordPlan("x", planDir, "commit_fail")
	requireSinglePrefixRefusal(t, err, `RecordPlan: write blob "`)
	if ops != nil {
		t.Fatalf("ops = %v, want nil on a commit failure", ops)
	}
	// The real on-disk state: the blob copied before the failure is in place
	// (partial update, as documented), the blocker is untouched, no plan.jsonl.
	if got := mustRead(t, filepath.Join(planDir, filepath.FromSlash(glob), "f1")); got != "glob version one\n" {
		t.Fatalf("blob copied before the failure = %q", got)
	}
	if got := mustRead(t, blocker); got != "blocks the replace" {
		t.Fatalf("blocking directory content = %q, must be untouched", got)
	}
	if _, err := os.Lstat(filepath.Join(planDir, "plan.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("plan.jsonl exists (%v); RecordPlan never writes it", err)
	}
	assertNoStagingLeft(t, tmp)
}

// probeGlobAndBigRefs learns the deterministic blob refs of the glob and the
// big-file blob of the currently registered plan by recording it once into a
// scratch directory.
func probeGlobAndBigRefs(t *testing.T) (glob, big string) {
	t.Helper()
	probe, err := RecordPlan("x", filepath.Join(t.TempDir(), "probe"), "commit_fail")
	if err != nil {
		t.Fatalf("probe record: %v", err)
	}
	for _, op := range probe {
		switch {
		case strings.HasPrefix(op.Blob, "blobs/synced-"):
			glob = op.Blob
		case strings.HasPrefix(op.Blob, "blobs/big-"):
			big = op.Blob
		}
	}
	if glob == "" || big == "" {
		t.Fatalf("probe refs glob=%q big=%q", glob, big)
	}
	return glob, big
}

// TestCopyStagedBlobRefMismatch covers the defensive got != ref branch: a
// staged name the destination store would sanitize differently is an error, not
// a silently different ref (the ops would then point at a blob that is not
// where the plan says).
func TestCopyStagedBlobRefMismatch(t *testing.T) {
	stage := t.TempDir()
	if err := os.MkdirAll(filepath.Join(stage, "blobs"), 0o700); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(stage, "blobs", "has space"), []byte("x"))
	dest := t.TempDir()
	err := copyStagedBlob(stage, plan.NewStore(dest), "blobs/has space")
	if err == nil || !strings.Contains(err.Error(), `was written as "blobs/has-space"`) {
		t.Fatalf("copyStagedBlob error = %v, want a ref mismatch error", err)
	}
	// The blob landed under the sanitized name; the error is what protects the
	// caller from returning ops that reference "blobs/has space".
	if got := mustRead(t, filepath.Join(dest, "blobs", "has-space")); got != "x" {
		t.Fatalf("blob under the sanitized ref = %q", got)
	}
}

// TestCommitStagedBlobsMissingStagedBlob: an op referencing a blob the staging
// directory does not hold (or no staging directory at all) is reported, never
// skipped.
func TestCommitStagedBlobsMissingStagedBlob(t *testing.T) {
	planDir := filepath.Join(t.TempDir(), "out")
	for _, stage := range []string{t.TempDir(), ""} {
		err := commitStagedBlobs([]plan.Op{{Blob: "blobs/gone"}}, stage, planDir)
		if err == nil {
			t.Fatalf("stage %q: want an error for a missing staged blob", stage)
		}
	}
}

// planDirProbe registers a task that records whether its body ran, so a test
// can tell an up-front refusal (body never ran) from a late one.
func planDirProbe(name string) *bool {
	ran := new(bool)
	Task(name, "", func() { *ran = true })
	return ran
}

// unusablePlanDir is one plan directory checkPlanDirUsable must refuse before
// any task body runs.
type unusablePlanDir struct {
	name    string
	path    string
	want    string // substring of the refusal
	nonRoot bool   // only refused for an unprivileged user (access(2) is not enforced for root)
	skip    string // non-empty: the environment cannot exercise this case; the subtest skips with this reason
}

// chmodedDir creates dir with exactly mode (mkdir applies the umask, so it
// chmods afterwards) and the caller's own group, and returns it.
func chmodedDir(t *testing.T, dir string, mode os.FileMode) string {
	t.Helper()
	return testutil.MkdirMode(t, dir, mode)
}

// unusablePlanDirs builds the up-front refusal table below root. Every case
// lives inside root, and link points at root itself, so an attempt to create
// anything through the symlink shows up as a change of root's snapshot.
func unusablePlanDirs(t *testing.T, root string) []unusablePlanDir {
	t.Helper()
	file := filepath.Join(root, "a-file")
	mustWrite(t, file, []byte("not a dir"))
	readonly := filepath.Join(root, "readonly")
	if err := os.Mkdir(readonly, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(readonly, 0o700) })
	worldW := chmodedDir(t, filepath.Join(root, "world-writable"), 0o707)
	sticky := chmodedDir(t, filepath.Join(root, "sticky-world-writable"), 0o777|os.ModeSticky)
	link := filepath.Join(root, "link")
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	const symlinkRefusal = "is a symlink; symlinked plan directories are refused"
	const worldWritableRefusal = "world-writable"
	cases := []unusablePlanDir{
		{"target is a file", file, "is not a directory", false, ""},
		{"existing dir is read-only", readonly, "cannot write to", true, ""},
		{"existing dir is world-writable", worldW, worldWritableRefusal, false, ""},
		{"existing dir is sticky and world-writable (like /tmp)", sticky, worldWritableRefusal, false, ""},
		{"target is a symlink", link, symlinkRefusal, false, ""},
		{"target is a symlink with a trailing slash", link + "/", symlinkRefusal, false, ""},
		{"parent of an absent dir is a symlink", filepath.Join(link, "newsub"), symlinkRefusal, false, ""},
		{"ancestor of an absent dir is a symlink", filepath.Join(link, "newsub", "deeper"), symlinkRefusal, false, ""},
		{"parent of an absent dir is a file", filepath.Join(file, "sub"), "not a directory", false, ""},
		{"absent dir in a read-only parent", filepath.Join(readonly, "sub", "deeper"), "cannot create", true, ""},
	}
	// Group write is refused unless the group is the caller's private group, so
	// this case needs a group of the caller's that is not its own. The case
	// always stays in the table; where the runner cannot chgrp, its subtest
	// skips with the reason (and only that subtest).
	shared := chmodedDir(t, filepath.Join(root, "shared-group-writable"), 0o700)
	_, skip := testutil.TryChgrpForeign(shared)
	if err := os.Chmod(shared, 0o2775); err != nil {
		t.Fatal(err)
	}
	return append(cases, unusablePlanDir{"existing dir is group-writable by a shared group", shared, "group-writable by group", false, skip})
}

// TestRecordPlanRefusesUnusablePlanDirUpFront pins the early failure the old
// SecureDir-first order gave for the common mistakes: a planDir SecureDir would
// refuse (a symlink, a symlinked ancestor, a file below which nothing can be
// created, a world- or shared-group-writable directory, an unwritable location) fails
// BEFORE any task body runs, with a message that says what is wrong, and the
// check itself creates or changes nothing (the snapshot includes modes, so a
// chmod of the refused directory would show). Only the read-only cases need an
// unprivileged user; the others run as root too.
func TestRecordPlanRefusesUnusablePlanDirUpFront(t *testing.T) {
	root := t.TempDir()
	for _, tc := range unusablePlanDirs(t, root) {
		t.Run(tc.name, func(t *testing.T) {
			if tc.skip != "" {
				t.Skip(tc.skip)
			}
			if tc.nonRoot && os.Geteuid() == 0 {
				t.Skip("permission checks are not enforced for root")
			}
			ResetForTest()
			t.Cleanup(ResetForTest)
			ran := planDirProbe("probe")
			before := testutil.Snapshot(t, root)
			_, err := RecordPlan("x", tc.path, "probe")
			if err == nil || !strings.Contains(err.Error(), "RecordPlan: plan dir") || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("RecordPlan error = %v, want a \"RecordPlan: plan dir\" error containing %q", err, tc.want)
			}
			if *ran {
				t.Fatal("the task body ran; an unusable plan dir must fail before any body")
			}
			testutil.RequireUnchanged(t, before, root)
		})
	}
}

// TestCommitStagedBlobsRefusesSymlinkedPlanDir pins the backstop behind the
// best-effort pre-check: even when a symlinked plan directory or ancestor gets
// past checkPlanDirUsable (a path swapped after the check), SecureDir refuses
// it at commit time and nothing is written through the link. commitStagedBlobs
// is called directly, so the pre-check cannot mask the result.
func TestCommitStagedBlobsRefusesSymlinkedPlanDir(t *testing.T) {
	root := t.TempDir()
	link := filepath.Join(root, "link")
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	stage := t.TempDir()
	if err := os.MkdirAll(filepath.Join(stage, "blobs"), 0o700); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(stage, "blobs", "b"), []byte("blob"))
	ops := []plan.Op{{Blob: "blobs/b"}}
	for name, planDir := range map[string]string{
		"symlink":                   link,
		"symlink with a slash":      link + "/",
		"symlinked ancestor":        filepath.Join(link, "newsub"),
		"deeper symlinked ancestor": filepath.Join(link, "newsub", "deeper"),
	} {
		t.Run(name, func(t *testing.T) {
			before := testutil.Snapshot(t, root)
			if err := commitStagedBlobs(ops, stage, planDir); err == nil {
				t.Fatal("want SecureDir to refuse a symlinked plan directory")
			}
			testutil.RequireUnchanged(t, before, root)
		})
	}
}

// TestCommitStagedBlobsRefusesWritablePlanDir pins the backstop for the
// unsafe-mode refusal: even when a world- or shared-group-writable plan
// directory gets past the best-effort pre-check (the mode changed after it
// ran), commitStagedBlobs refuses it and writes no blob into it. It is called
// directly, so the pre-check cannot mask the result.
func TestCommitStagedBlobsRefusesWritablePlanDir(t *testing.T) {
	stage := t.TempDir()
	if err := os.MkdirAll(filepath.Join(stage, "blobs"), 0o700); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(stage, "blobs", "b"), []byte("blob"))
	ops := []plan.Op{{Blob: "blobs/b"}}
	type unsafeDir struct{ name, path, want, skip string }
	var dirs []unsafeDir
	for _, mode := range []os.FileMode{0o757, 0o777, 0o777 | os.ModeSticky} {
		planDir := chmodedDir(t, filepath.Join(t.TempDir(), "out"), mode)
		dirs = append(dirs, unsafeDir{mode.String(), planDir, "world-writable", ""})
	}
	// The shared-group case stays in the table; its subtest skips, with the
	// reason, where the runner has no group it can chgrp to.
	shared := chmodedDir(t, filepath.Join(t.TempDir(), "out"), 0o700)
	_, skip := testutil.TryChgrpForeign(shared)
	if err := os.Chmod(shared, 0o775); err != nil {
		t.Fatal(err)
	}
	dirs = append(dirs, unsafeDir{"0775 shared group", shared, "group-writable by group", skip})
	for _, d := range dirs {
		t.Run(d.name, func(t *testing.T) {
			if d.skip != "" {
				t.Skip(d.skip)
			}
			before := testutil.Snapshot(t, d.path)
			err := commitStagedBlobs(ops, stage, d.path)
			if err == nil || !strings.Contains(err.Error(), d.want) || !strings.Contains(err.Error(), "RecordPlan: plan dir: ") {
				t.Fatalf("commitStagedBlobs into a %s directory = %v, want a %q refusal", d.name, err, d.want)
			}
			testutil.RequireUnchanged(t, before, d.path)
		})
	}
}

// foreignOwnedDir returns a real (non-symlink) system directory that the
// current user does not own, or skips the test. System directories are
// root-owned everywhere gonf runs, so no chown (and no privilege) is needed.
func foreignOwnedDir(t *testing.T) string {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root may take over any directory, so nothing is foreign to it")
	}
	for _, dir := range []string{"/usr", "/etc", "/opt"} {
		info, err := os.Lstat(dir)
		if err != nil || !info.IsDir() {
			continue // missing, or a symlink on this system
		}
		if st, ok := info.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Geteuid() {
			return dir
		}
	}
	t.Skip("no root-owned system directory found")
	return ""
}

// TestCheckPlanDirUsableRefusesForeignOwner covers the "owned by another user"
// refusal: SecureDir only accepts an existing plan directory that the
// effective user owns (its owner could otherwise swap plan.jsonl), so a
// foreign one is refused up front. checkPlanDirUsable is called directly (it
// only inspects the path) so the test can never modify the system directory it
// uses as the example.
func TestCheckPlanDirUsableRefusesForeignOwner(t *testing.T) {
	dir := foreignOwnedDir(t)
	err := checkPlanDirUsable(dir)
	if err == nil || !strings.Contains(err.Error(), dir+" is owned by uid") {
		t.Fatalf("checkPlanDirUsable(%s) = %v, want an \"is owned by uid\" refusal", dir, err)
	}
}

// TestRecordPlanAcceptsUsablePlanDirs is the negative twin of the up-front
// check: existing directories we own that are not writable by others (0700, and
// a 0755 one, the "-o ." checkout case; a 0775 one of our private group for a
// user-private-group user) and absent ones (nested, under a
// writable ancestor) pass and record normally. An existing directory keeps its
// exact mode (SecureDir verifies it, m62: it used to chmod it to 0700), a
// directory the record creates is 0700. Every directory is its own subtest; the
// 0775 private-group one skips, with a reason, unless the runner is an
// unprivileged user-private-group user (the rule itself is covered for every
// identity by the synthetic tests in package plan).
func TestRecordPlanAcceptsUsablePlanDirs(t *testing.T) {
	root := t.TempDir()
	cases := []struct {
		name string
		dir  string
		want os.FileMode
		skip string
	}{
		{"existing 0700", chmodedDir(t, filepath.Join(root, "private"), 0o700), 0o700, ""},
		{"existing 0755", chmodedDir(t, filepath.Join(root, "readable"), 0o755), 0o755, ""},
		{"absent", filepath.Join(root, "absent"), 0o700, ""},
		{"absent and nested", filepath.Join(root, "a", "b", "c"), 0o700, ""},
		// A UPG checkout: mkdir under umask 002 gives 0775 with the user's own group.
		{"existing 0775 of the private group", chmodedDir(t, filepath.Join(root, "upg-checkout"), 0o775), 0o775, testutil.PrivateGroupSkipReason()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.skip != "" {
				t.Skip(tc.skip)
			}
			ResetForTest()
			t.Cleanup(ResetForTest)
			ran := planDirProbe("probe")
			if _, err := RecordPlan("x", tc.dir, "probe"); err != nil {
				t.Fatalf("RecordPlan(%s): %v", tc.dir, err)
			}
			if !*ran {
				t.Fatalf("RecordPlan(%s): body did not run", tc.dir)
			}
			if fi, err := os.Stat(tc.dir); err != nil || fi.Mode().Perm() != tc.want {
				t.Fatalf("plan dir %s after record: %v, %v; want mode %04o", tc.dir, fi, err, tc.want)
			}
		})
	}
}

// TestRecordPlanLeavesExistingDirModeAlone is the m62 regression at the API:
// RecordPlan into an existing plan directory, blobs included, keeps the
// directory's mode (0755, the read-only-for-others 0750 and the private 0700)
// while the blobs directory it creates inside is 0700. A read-only directory
// (0555, 0500) is not part of this table: the api refuses it up front, before
// any body runs (TestRecordPlanReadOnlyPlanDirSaysHowToFixIt).
func TestRecordPlanLeavesExistingDirModeAlone(t *testing.T) {
	src := t.TempDir()
	mustWrite(t, filepath.Join(src, "f"), []byte("payload"))
	for _, mode := range []os.FileMode{0o755, 0o750, 0o700} {
		t.Run(mode.String(), func(t *testing.T) {
			planDir := chmodedDir(t, filepath.Join(t.TempDir(), "out"), mode)
			ResetForTest()
			t.Cleanup(ResetForTest)
			Task("tree", "", func() { Dir(filepath.Join(t.TempDir(), "dst"), options.WithSource(src)) })
			if _, err := RecordPlan("x", planDir, "tree"); err != nil {
				t.Fatal(err)
			}
			if fi, err := os.Stat(planDir); err != nil || fi.Mode().Perm() != mode {
				t.Fatalf("plan dir after record: %v, %v; want mode %04o unchanged", fi, err, mode)
			}
			if fi, err := os.Stat(filepath.Join(planDir, "blobs")); err != nil || fi.Mode().Perm() != 0o700 {
				t.Fatalf("blobs dir after record: %v, %v; want the created directory 0700", fi, err)
			}
		})
	}
}
