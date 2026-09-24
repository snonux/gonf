package api

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestRecordPlanDeferredCommitsLikeRecordPlan pins task 5b2's deferred
// path: RecordPlanDeferred writes nothing to planDir while recording, and
// CommitBlobs then writes every blob kind (glob, tree with an empty
// directory and a symlink, single file over the inline limit) exactly as
// RecordPlan's staging commit writes them, with the same ops.
func TestRecordPlanDeferredCommitsLikeRecordPlan(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)
	src := newStagedSources(t)
	src.register("deferred_ok", nil)

	stagedDir := filepath.Join(t.TempDir(), "staged")
	want, err := RecordPlan("p", stagedDir, "deferred_ok")
	if err != nil {
		t.Fatalf("RecordPlan: %v", err)
	}
	deferredDir := filepath.Join(t.TempDir(), "deferred")
	d, err := RecordPlanDeferred("p", deferredDir, "deferred_ok")
	if err != nil {
		t.Fatalf("RecordPlanDeferred: %v", err)
	}
	if _, err := os.Lstat(deferredDir); !os.IsNotExist(err) {
		t.Fatalf("planDir exists before CommitBlobs (err=%v); recording must not write it", err)
	}
	if !reflect.DeepEqual(d.Ops, want) {
		t.Fatalf("deferred ops differ from RecordPlan's:\n%+v\nwant\n%+v", d.Ops, want)
	}
	if err := d.CommitBlobs(); err != nil {
		t.Fatalf("CommitBlobs: %v", err)
	}
	assertCommittedBlobs(t, deferredDir, blobRefsByBase(t, d.Ops))
	if a, b := dirListing(t, stagedDir), dirListing(t, deferredDir); a != b {
		t.Fatalf("committed trees differ:\nRecordPlan:\n%s\nCommitBlobs:\n%s", a, b)
	}
}

// TestRecordPlanDeferredRefusesUnusablePlanDir: the up-front plan
// directory check runs before any task body, as RecordPlan's does.
func TestRecordPlanDeferredRefusesUnusablePlanDir(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)
	ran := false
	Task("deferred_probe", "", func() { ran = true })
	file := filepath.Join(t.TempDir(), "not-a-dir")
	mustWrite(t, file, []byte("x"))
	_, err := RecordPlanDeferred("p", file, "deferred_probe")
	if err == nil || !strings.HasPrefix(err.Error(), "RecordPlan: plan dir: ") || ran {
		t.Fatalf("err = %v, body ran = %v; want an up-front plan dir refusal", err, ran)
	}
}

// dirListing renders every entry below root (relative path, mode, a
// symlink's target and a file's content) in walk order.
func dirListing(t *testing.T, root string) string {
	t.Helper()
	var b strings.Builder
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		b.WriteString(rel + " " + info.Mode().String())
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			b.WriteString(" -> " + target)
		case info.Mode().IsRegular():
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			b.WriteString(" " + string(data))
		}
		b.WriteString("\n")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return b.String()
}
