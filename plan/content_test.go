package plan

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDecodeContentB64Valid(t *testing.T) {
	want := []byte("hello plan\n")
	got, err := DecodeContentB64(base64.StdEncoding.EncodeToString(want))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestDecodeContentB64Corrupt(t *testing.T) {
	_, err := DecodeContentB64("not!!base64")
	if err == nil || !strings.Contains(err.Error(), "corrupt base64") {
		t.Fatalf("want corrupt base64 error, got %v", err)
	}
}

func TestResolveMissingBlobID(t *testing.T) {
	_, err := Resolve(t.TempDir(), "")
	if err == nil || !strings.Contains(err.Error(), "missing blob id") {
		t.Fatalf("want missing blob id error, got %v", err)
	}

	_, err = Resolve(t.TempDir(), "blobs/does-not-exist")
	if err == nil || !strings.Contains(err.Error(), "missing blob") {
		t.Fatalf("want missing blob error, got %v", err)
	}
}

func TestApplyFileContentB64AndCorrupt(t *testing.T) {
	root := t.TempDir()
	dst := filepath.Join(root, "taskrc")
	ops := []Op{
		header(),
		{
			Op:         KindFile,
			Path:       dst,
			Mode:       "0640",
			ContentB64: base64.StdEncoding.EncodeToString([]byte("set x=1\n")),
		},
	}
	if err := Apply(ops, Facts{}, ""); err != nil {
		t.Fatalf("valid content_b64: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "set x=1\n" {
		t.Fatalf("content = %q", got)
	}

	bad := []Op{
		header(),
		{Op: KindFile, Path: filepath.Join(root, "bad"), ContentB64: "@@@@"},
	}
	err = Apply(bad, Facts{}, "")
	if err == nil || !strings.Contains(err.Error(), "corrupt base64") {
		t.Fatalf("want corrupt base64 error, got %v", err)
	}
}

// TestApplyFileHasContentAllowsEmpty pins the k5 fix: a KindFile op recording
// legitimately empty content (content_b64 == "" but has_content == true, the
// wire shape for WithContent("") or an empty WithSource file) must apply as
// an empty file instead of failing "missing content_b64 and blob".
func TestApplyFileHasContentAllowsEmpty(t *testing.T) {
	root := t.TempDir()
	dst := filepath.Join(root, "empty.conf")
	ops := []Op{
		header(),
		{
			Op:         KindFile,
			Path:       dst,
			Mode:       "0640",
			ContentB64: "",
			HasContent: true,
		},
	}
	if err := Apply(ops, Facts{}, ""); err != nil {
		t.Fatalf("empty content_b64 with has_content: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("content = %q, want empty", got)
	}
}

// TestApplyFileMissingContentStillErrors pins the other half of the k5 fix:
// an op with neither content_b64 nor blob AND has_content unset (the
// record-time-bug case: content data never made it onto the wire) must still
// fail loudly instead of silently writing an empty file.
func TestApplyFileMissingContentStillErrors(t *testing.T) {
	root := t.TempDir()
	ops := []Op{
		header(),
		{Op: KindFile, Path: filepath.Join(root, "missing.conf"), Mode: "0640"},
	}
	err := Apply(ops, Facts{}, "")
	if err == nil || !strings.Contains(err.Error(), "missing content_b64 and blob") {
		t.Fatalf("want missing content_b64 and blob error, got %v", err)
	}
}

func TestApplyFileAndSyncDirBlobs(t *testing.T) {
	planDir := t.TempDir()
	store := NewStore(planDir)

	fileRef, err := store.WriteFile("gitconfig", []byte("user.name=x\n"))
	if err != nil {
		t.Fatal(err)
	}
	srcTree := filepath.Join(planDir, "src-tree")
	if err := os.MkdirAll(srcTree, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcTree, "a.conf"), []byte("a\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	treeRef, err := store.WriteTree("app", srcTree)
	if err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	fileDst := filepath.Join(root, "gitconfig")
	dirDst := filepath.Join(root, "app")

	ops := []Op{
		header(),
		{Op: KindFile, Path: fileDst, Mode: "0640", Blob: fileRef},
		{Op: KindSyncDir, Path: dirDst, Mode: "0700", Blob: treeRef, Prune: true, Payload: SyncDirPayload{FileMode: "0640"}},
	}
	if err := Apply(ops, Facts{}, planDir); err != nil {
		t.Fatalf("apply blobs: %v", err)
	}
	got, err := os.ReadFile(fileDst)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "user.name=x\n" {
		t.Fatalf("file blob content = %q", got)
	}
	got, err = os.ReadFile(filepath.Join(dirDst, "a.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "a\n" {
		t.Fatalf("sync_dir content = %q", got)
	}

	missing := []Op{
		header(),
		{Op: KindSyncDir, Path: filepath.Join(root, "missing"), Blob: "blobs/nope"},
	}
	err = Apply(missing, Facts{}, planDir)
	if err == nil || !strings.Contains(err.Error(), "missing blob") {
		t.Fatalf("want missing blob error, got %v", err)
	}
}

func TestApplyFileMissingBlobID(t *testing.T) {
	ops := []Op{
		header(),
		{Op: KindFile, Path: filepath.Join(t.TempDir(), "x"), Blob: "blobs/gone"},
	}
	err := Apply(ops, Facts{}, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "missing blob") {
		t.Fatalf("want missing blob error, got %v", err)
	}
}
