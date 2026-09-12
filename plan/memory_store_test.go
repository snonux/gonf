package plan

import (
	"os"
	"path/filepath"
	"testing"
)

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
	if string(tree["a.conf"]) != "a\n" || string(tree["sub/b.conf"]) != "b\n" {
		t.Fatalf("tree = %#v", tree)
	}
	if !m.HasBlobs() {
		t.Fatal("HasBlobs")
	}
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
	if !ok || string(tree["x.service"]) != "[Unit]\n" {
		t.Fatalf("tree = %#v", tree)
	}
	if _, exists := tree["skip"]; exists {
		t.Fatal("dirs must not be packaged by WriteGlob")
	}
}

func TestMemoryStoreNilRejected(t *testing.T) {
	var m *MemoryStore
	_, err := m.WriteFile("x", []byte("y"))
	if err == nil {
		t.Fatal("expected error for nil store")
	}
}
