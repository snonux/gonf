package plan

import (
	"archive/tar"
	"bytes"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestEncodeDecodePushNoBlobs(t *testing.T) {
	ops := []Op{
		{Op: KindPlan, Version: CurrentVersion, ID: "p"},
		{Op: KindFile, Path: "/tmp/x", ContentB64: "eA=="},
	}
	var buf bytes.Buffer
	if err := EncodePush(&buf, ops, nil); err != nil {
		t.Fatal(err)
	}
	got, err := DecodePush(&buf, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Ops) != 2 || got.Ops[0].ID != "p" || got.PlanDir != "" {
		t.Fatalf("%#v", got)
	}
}

func TestEncodeDecodePushWithMemoryBlobs(t *testing.T) {
	mem := NewMemoryStore()
	ref, err := mem.WriteFile("secret", []byte("sekrit\n"))
	if err != nil {
		t.Fatal(err)
	}
	ops := []Op{
		{Op: KindPlan, Version: CurrentVersion, ID: "p"},
		{Op: KindFile, Path: "/tmp/secret", Blob: ref},
	}
	var buf bytes.Buffer
	if err := EncodePush(&buf, ops, mem); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	got, err := DecodePush(&buf, dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.PlanDir != dir {
		t.Fatalf("planDir=%q", got.PlanDir)
	}
	data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(ref)))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "sekrit\n" {
		t.Fatalf("blob data %q", data)
	}
	info, err := os.Stat(filepath.Join(dir, filepath.FromSlash(ref)))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("mode %#o", info.Mode().Perm())
	}
}

// fakeBlobReader is a minimal BlobReader that is not *MemoryStore, proving
// EncodePush depends only on the interface (DIP): the sole write path
// (WriteFile/WriteTree/WriteGlob) stays MemoryStore/Store-specific, but the
// push-time read-back works against any BlobReader implementation.
type fakeBlobReader struct {
	files map[string][]byte
}

func (f *fakeBlobReader) HasBlobs() bool { return len(f.files) > 0 }
func (f *fakeBlobReader) Refs() []string {
	refs := make([]string, 0, len(f.files))
	for ref := range f.files {
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	return refs
}
func (f *fakeBlobReader) FileBlob(ref string) ([]byte, bool) {
	data, ok := f.files[ref]
	return data, ok
}
func (f *fakeBlobReader) TreeBlob(ref string) ([]BlobEntry, bool) { return nil, false }

var _ BlobReader = (*fakeBlobReader)(nil)

func TestEncodeDecodePushWithFakeBlobReader(t *testing.T) {
	mem := &fakeBlobReader{files: map[string][]byte{
		"blobs/secret": []byte("sekrit\n"),
	}}
	ops := []Op{
		{Op: KindPlan, Version: CurrentVersion, ID: "p"},
		{Op: KindFile, Path: "/tmp/secret", Blob: "blobs/secret"},
	}
	var buf bytes.Buffer
	if err := EncodePush(&buf, ops, mem); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	got, err := DecodePush(&buf, dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.PlanDir != dir {
		t.Fatalf("planDir=%q", got.PlanDir)
	}
	data, err := os.ReadFile(filepath.Join(dir, "blobs", "secret"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "sekrit\n" {
		t.Fatalf("blob data %q", data)
	}
}

func TestDecodePushBareJSONL(t *testing.T) {
	raw, err := EncodePlan([]Op{{Op: KindPlan, Version: CurrentVersion, ID: "bare"}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodePush(bytes.NewReader(raw), "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Ops[0].ID != "bare" {
		t.Fatalf("%#v", got.Ops)
	}
}

func TestDecodePushRejectsZipSlip(t *testing.T) {
	dir := t.TempDir()
	err := extractTarHeader(dir, &tar.Header{Name: "../evil", Typeflag: tar.TypeReg, Size: 0}, bytes.NewReader(nil))
	if err == nil || !strings.Contains(err.Error(), "zip-slip") {
		t.Fatalf("want zip-slip error, got %v", err)
	}
}

func TestDecodePushBadMagic(t *testing.T) {
	_, err := DecodePush(strings.NewReader("NOPE\nblobs 0\nplan\n"), "")
	if err == nil || !strings.Contains(err.Error(), "bad magic") {
		t.Fatalf("want bad magic, got %v", err)
	}
}
