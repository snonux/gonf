package plan

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStoreOwnerOnlyModes(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root)

	ref, err := store.WriteFile("secret", []byte("x\n"))
	if err != nil {
		t.Fatal(err)
	}
	assertOwnerOnlyFile(t, filepath.Join(root, filepath.FromSlash(ref)))
	assertOwnerOnlyDir(t, filepath.Join(root, "blobs"))

	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "a"), []byte("a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	treeRef, err := store.WriteTree("tree", src)
	if err != nil {
		t.Fatal(err)
	}
	treeAbs := filepath.Join(root, filepath.FromSlash(treeRef))
	assertOwnerOnlyDir(t, treeAbs)
	assertOwnerOnlyFile(t, filepath.Join(treeAbs, "a"))
}

func assertOwnerOnlyFile(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	perm := info.Mode().Perm()
	if perm&0o077 != 0 {
		t.Fatalf("%s mode %#o has group/other bits", path, perm)
	}
	if perm&0o600 != 0o600 {
		t.Fatalf("%s mode %#o missing owner rw", path, perm)
	}
}

func assertOwnerOnlyDir(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	perm := info.Mode().Perm()
	if perm&0o077 != 0 {
		t.Fatalf("%s mode %#o has group/other bits", path, perm)
	}
	if perm&0o700 != 0o700 {
		t.Fatalf("%s mode %#o missing owner rwx", path, perm)
	}
}
