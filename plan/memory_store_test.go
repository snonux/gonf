package plan

import (
	"os"
	"path/filepath"
	"testing"
)

// findTreeEntry returns the manifest entry with the given slash-separated
// Rel, failing when absent.
func findTreeEntry(t *testing.T, entries []BlobEntry, rel string) BlobEntry {
	t.Helper()
	for _, e := range entries {
		if e.Rel == rel {
			return e
		}
	}
	t.Fatalf("entry %q not found in %#v", rel, entries)
	return BlobEntry{}
}

func TestMemoryStoreWriteFileAndTree(t *testing.T) {
	m := NewMemoryStore()
	ref, err := m.WriteFile("secret", []byte("top-secret\n"))
	if err != nil {
		t.Fatal(err)
	}
	if ref != "blobs/secret" {
		t.Fatalf("ref = %q", ref)
	}
	got, ok := m.FileBlob(ref)
	if !ok || string(got) != "top-secret\n" {
		t.Fatalf("file blob = %q ok=%v", got, ok)
	}

	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "a.conf"), []byte("a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(src, "sub")
	if err := os.MkdirAll(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "b.conf"), []byte("b\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	treeRef, err := m.WriteTree("app", src)
	if err != nil {
		t.Fatal(err)
	}
	tree, ok := m.TreeBlob(treeRef)
	if !ok {
		t.Fatal("missing tree")
	}
	if e := findTreeEntry(t, tree, "a.conf"); e.Kind != BlobFile || string(e.Data) != "a\n" {
		t.Fatalf("a.conf = %#v", e)
	}
	if e := findTreeEntry(t, tree, "sub/b.conf"); e.Kind != BlobFile || string(e.Data) != "b\n" {
		t.Fatalf("sub/b.conf = %#v", e)
	}
	if e := findTreeEntry(t, tree, "sub"); e.Kind != BlobDir {
		t.Fatalf("sub = %#v", e)
	}
	if !m.HasBlobs() {
		t.Fatal("HasBlobs")
	}
}

func TestMemoryStoreWriteTreePreservesSymlinksAndEmptyDirs(t *testing.T) {
	m := NewMemoryStore()
	src := buildManifestSourceTree(t)

	treeRef, err := m.WriteTree("app", src)
	if err != nil {
		t.Fatal(err)
	}
	tree, ok := m.TreeBlob(treeRef)
	if !ok {
		t.Fatal("missing tree")
	}
	assertManifestEntries(t, tree)
}

func TestMemoryStoreWriteGlob(t *testing.T) {
	m := NewMemoryStore()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "x.service"), []byte("[Unit]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "skip"), 0o700); err != nil {
		t.Fatal(err)
	}
	ref, err := m.WriteGlob("units", filepath.Join(dir, "*"))
	if err != nil {
		t.Fatal(err)
	}
	tree, ok := m.TreeBlob(ref)
	if !ok {
		t.Fatal("missing tree")
	}
	if e := findTreeEntry(t, tree, "x.service"); e.Kind != BlobFile || string(e.Data) != "[Unit]\n" {
		t.Fatalf("x.service = %#v", e)
	}
	for _, e := range tree {
		if e.Rel == "skip" {
			t.Fatal("dirs must not be packaged by WriteGlob")
		}
	}
}

func TestMemoryStoreWriteGlobFlatPolicy(t *testing.T) {
	m := NewMemoryStore()
	dir := t.TempDir()
	if err := writeGlobFixture(dir); err != nil {
		t.Fatal(err)
	}
	ref, err := m.WriteGlob("units", filepath.Join(dir, "*"))
	if err != nil {
		t.Fatal(err)
	}
	tree, ok := m.TreeBlob(ref)
	if !ok {
		t.Fatal("missing tree")
	}
	want := globFixtureWant()
	if len(tree) != len(want) {
		t.Fatalf("glob manifest = %#v, want %#v", tree, want)
	}
	for i, e := range tree {
		if e.Rel != want[i].Rel || e.Kind != want[i].Kind || e.Target != want[i].Target ||
			string(e.Data) != string(want[i].Data) {
			t.Fatalf("glob manifest[%d] = %#v, want %#v", i, e, want[i])
		}
	}
}

func TestMemoryStoreNilRejected(t *testing.T) {
	var m *MemoryStore
	_, err := m.WriteFile("x", []byte("y"))
	if err == nil {
		t.Fatal("expected error for nil store")
	}
}
