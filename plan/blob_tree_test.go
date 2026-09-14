package plan

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestStoreWriteTreePreservesSymlinksAndEmptyDirs(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root)
	src := buildManifestSourceTree(t)

	ref, err := store.WriteTree("app", src)
	if err != nil {
		t.Fatal(err)
	}
	treeAbs := filepath.Join(root, filepath.FromSlash(ref))
	assertDiskTree(t, treeAbs, manifestWanted())
	assertOwnerOnlyDir(t, treeAbs)
	assertOwnerOnlyDir(t, filepath.Join(root, "blobs"))
}

func TestStoreWriteGlobPreservesSymlinks(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root)
	src := t.TempDir()
	if err := writeGlobFixture(src); err != nil {
		t.Fatal(err)
	}

	ref, err := store.WriteGlob("units", filepath.Join(src, "*"))
	if err != nil {
		t.Fatal(err)
	}
	assertGlobDiskTree(t, filepath.Join(root, filepath.FromSlash(ref)), globFixtureWant())
}

// TestStoreWriteTreeScanFailsBeforeClear pins the fail-first ordering: a
// packaging error (unsupported file type) fails during scanTree, BEFORE
// the destination blob is cleared — a pre-existing packaged blob survives.
func TestStoreWriteTreeScanFailsBeforeClear(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root)
	if _, err := store.WriteFile("app", []byte("keep\n")); err != nil {
		t.Fatal(err)
	}
	src := t.TempDir()
	if err := makeFifo(filepath.Join(src, "fifo")); err != nil {
		t.Skipf("cannot create fifo: %v", err)
	}
	if _, err := store.WriteTree("app", src); err == nil ||
		!strings.Contains(err.Error(), "unsupported file type") {
		t.Fatalf("want unsupported-type error, got %v", err)
	}
	data, err := ReadFile(root, "blobs/app")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "keep\n" {
		t.Fatalf("pre-existing blob was cleared: %q", data)
	}
}

func TestStoreWriteTreeMissingSource(t *testing.T) {
	store := NewStore(t.TempDir())
	missing := filepath.Join(t.TempDir(), "nope")
	if _, err := store.WriteTree("app", missing); err == nil ||
		!strings.Contains(err.Error(), "package tree") {
		t.Fatalf("want package-tree error, got %v", err)
	}
}

// writeGlobFixture creates the canonical glob source set:
//
//	real      regular file "real\n"
//	adir      directory (skipped)
//	tofile    symlink -> "real"
//	dangling  dangling symlink -> "nowhere"
//	todir     symlink -> "adir"
func writeGlobFixture(src string) error {
	if err := os.WriteFile(filepath.Join(src, "real"), []byte("real\n"), 0o600); err != nil {
		return err
	}
	if err := os.Mkdir(filepath.Join(src, "adir"), 0o700); err != nil {
		return err
	}
	if err := os.Symlink("real", filepath.Join(src, "tofile")); err != nil {
		return err
	}
	if err := os.Symlink("nowhere", filepath.Join(src, "dangling")); err != nil {
		return err
	}
	return os.Symlink("adir", filepath.Join(src, "todir"))
}

// globFixtureWant returns the manifest writeGlobFixture must produce.
func globFixtureWant() []BlobEntry {
	return []BlobEntry{
		{Rel: "dangling", Kind: BlobSymlink, Target: "nowhere"},
		{Rel: "real", Kind: BlobFile, Data: []byte("real\n")},
		{Rel: "todir", Kind: BlobSymlink, Target: "adir"},
		{Rel: "tofile", Kind: BlobSymlink, Target: "real"},
	}
}

// assertGlobDiskTree pins a flat glob-packaged tree on disk (Lstat only).
func assertGlobDiskTree(t *testing.T, root string, want []BlobEntry) {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != len(want) {
		t.Fatalf("glob blob = %#v, want %#v", entries, want)
	}
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			t.Fatal(err)
		}
		var w *BlobEntry
		for i := range want {
			if want[i].Rel == entry.Name() {
				w = &want[i]
				break
			}
		}
		if w == nil {
			t.Fatalf("unexpected glob entry %q under %s", entry.Name(), root)
		}
		mode := info.Mode()
		switch w.Kind {
		case BlobSymlink:
			if mode&os.ModeSymlink == 0 {
				t.Fatalf("%s: want symlink, got mode %#o", entry.Name(), mode)
			}
			got, err := os.Readlink(filepath.Join(root, entry.Name()))
			if err != nil {
				t.Fatal(err)
			}
			if got != w.Target {
				t.Fatalf("%s: target %q, want %q", entry.Name(), got, w.Target)
			}
		default:
			if !mode.IsRegular() {
				t.Fatalf("%s: want regular file, got mode %#o", entry.Name(), mode)
			}
			data, err := os.ReadFile(filepath.Join(root, entry.Name()))
			if err != nil {
				t.Fatal(err)
			}
			if string(data) != string(w.Data) {
				t.Fatalf("%s: content %q, want %q", entry.Name(), data, w.Data)
			}
		}
	}
}

// makeFifo creates a named pipe for unsupported-type tests.
func makeFifo(path string) error {
	return syscall.Mkfifo(path, 0o600)
}
