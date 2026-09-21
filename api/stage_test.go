package api

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/snonux/gonf/api/options"
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
	// Blob refs are "blobs/<destination basename>-<hash>".
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

			// (a) absent directory stays absent.
			absent := filepath.Join(t.TempDir(), "not-yet")
			src.register("refused_absent", tc.bad)
			_, err := RecordPlan("refused", absent, "refused_absent")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("RecordPlan error = %v, want it to contain %q", err, tc.want)
			}
			testutil.RequireUnchanged(t, testutil.DirSnapshot{Absent: true}, absent)

			// (b) an earlier good plan survives byte for byte.
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
			assertNoStagingLeft(t, tmp)
		})
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
