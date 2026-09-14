package plan

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// buildManifestSourceTree creates the canonical parity source tree used by
// the scanTree, Store, MemoryStore, tar, and apply-parity tests:
//
//	a.conf        regular file "a\n"
//	dangling      dangling symlink -> "no-such-target"
//	empty.d/      empty directory
//	rel-link      relative symlink -> "a.conf"
//	sub/          directory
//	sub/b.conf    regular file "b\n"
//	sub/up-link   relative symlink -> "../a.conf"
func buildManifestSourceTree(t *testing.T) string {
	t.Helper()
	src := filepath.Join(t.TempDir(), "src")
	if err := os.MkdirAll(filepath.Join(src, "empty.d"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(src, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "a.conf"), []byte("a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "sub", "b.conf"), []byte("b\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("a.conf", filepath.Join(src, "rel-link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("no-such-target", filepath.Join(src, "dangling")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../a.conf", filepath.Join(src, "sub", "up-link")); err != nil {
		t.Fatal(err)
	}
	return src
}

// manifestWanted returns the exact manifest of buildManifestSourceTree.
func manifestWanted() []BlobEntry {
	return []BlobEntry{
		{Rel: "a.conf", Kind: BlobFile, Data: []byte("a\n")},
		{Rel: "dangling", Kind: BlobSymlink, Target: "no-such-target"},
		{Rel: "empty.d", Kind: BlobDir},
		{Rel: "rel-link", Kind: BlobSymlink, Target: "a.conf"},
		{Rel: "sub", Kind: BlobDir},
		{Rel: "sub/b.conf", Kind: BlobFile, Data: []byte("b\n")},
		{Rel: "sub/up-link", Kind: BlobSymlink, Target: "../a.conf"},
	}
}

// assertManifestEntries pins the canonical manifest: exact entries,
// payloads, and sort order.
func assertManifestEntries(t *testing.T, entries []BlobEntry) {
	t.Helper()
	want := manifestWanted()
	if len(entries) != len(want) {
		t.Fatalf("manifest = %#v, want %#v", entries, want)
	}
	for i, e := range entries {
		if e.Rel != want[i].Rel || e.Kind != want[i].Kind || e.Target != want[i].Target {
			t.Fatalf("manifest[%d] = %#v, want %#v", i, e, want[i])
		}
		if string(e.Data) != string(want[i].Data) {
			t.Fatalf("manifest[%d] data = %q, want %q", i, e.Data, want[i].Data)
		}
	}
}

// assertDiskTree pins a tree materialized at root: files by content,
// directories (empty ones included), symlinks raw — checked with Lstat
// semantics only (filepath.Walk does not follow symlinks; symlinks are
// verified via os.Readlink), so a read-through bug cannot hide.
func assertDiskTree(t *testing.T, root string, want []BlobEntry) {
	t.Helper()
	count := 0
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		count++
		var w *BlobEntry
		for i := range want {
			if want[i].Rel == rel {
				w = &want[i]
				break
			}
		}
		if w == nil {
			return fmt.Errorf("unexpected entry %q under %s", rel, root)
		}
		mode := info.Mode()
		switch w.Kind {
		case BlobDir:
			if !mode.IsDir() {
				return fmt.Errorf("%s: want dir, got mode %#o", path, mode)
			}
		case BlobSymlink:
			if mode&os.ModeSymlink == 0 {
				return fmt.Errorf("%s: want symlink, got mode %#o", path, mode)
			}
			got, err := os.Readlink(path)
			if err != nil {
				return err
			}
			if got != w.Target {
				return fmt.Errorf("%s: link target %q, want %q", path, got, w.Target)
			}
		default:
			if !mode.IsRegular() {
				return fmt.Errorf("%s: want regular file, got mode %#o", path, mode)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if string(data) != string(w.Data) {
				return fmt.Errorf("%s: content %q, want %q", path, data, w.Data)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if count != len(want) {
		t.Fatalf("walked %d entries under %s, want %d", count, root, len(want))
	}
}

// applicableManifest returns manifestWanted() without the dangling
// symlink: link.Ensure (the apply-side primitive behind sync_dir) treats
// dangling links as apply failures on BOTH transports by documented
// policy, so parity apply tests use the applicable subset.
func applicableManifest() []BlobEntry {
	var out []BlobEntry
	for _, e := range manifestWanted() {
		if e.Rel != "dangling" {
			out = append(out, e)
		}
	}
	return out
}

func TestScanTree(t *testing.T) {
	src := buildManifestSourceTree(t)
	entries, err := scanTree(src)
	if err != nil {
		t.Fatal(err)
	}
	assertManifestEntries(t, entries)
}

func TestScanTreeMissingAndNotADir(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope")
	if _, err := scanTree(missing); err == nil || !strings.Contains(err.Error(), "package tree") {
		t.Fatalf("want package tree error, got %v", err)
	}
	plain := filepath.Join(t.TempDir(), "plain")
	if err := os.WriteFile(plain, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := scanTree(plain); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("want not-a-directory error, got %v", err)
	}
}

func TestScanTreeRejectsUnsupportedFileType(t *testing.T) {
	src := t.TempDir()
	fifo := filepath.Join(src, "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("cannot create fifo: %v", err)
	}
	_, err := scanTree(src)
	if err == nil || !strings.Contains(err.Error(), "unsupported file type") || !strings.Contains(err.Error(), fifo) {
		t.Fatalf("want unsupported-type error naming the path, got %v", err)
	}
}

func TestScanGlob(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.conf"), []byte("a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.conf"), []byte("b\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("a.conf", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("nowhere", filepath.Join(dir, "dangling")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "skip.d"), 0o700); err != nil {
		t.Fatal(err)
	}
	entries, err := scanGlob(filepath.Join(dir, "*"))
	if err != nil {
		t.Fatal(err)
	}
	want := []BlobEntry{
		{Rel: "a.conf", Kind: BlobFile, Data: []byte("a\n")},
		{Rel: "b.conf", Kind: BlobFile, Data: []byte("b\n")},
		{Rel: "dangling", Kind: BlobSymlink, Target: "nowhere"},
		{Rel: "link", Kind: BlobSymlink, Target: "a.conf"},
	}
	if len(entries) != len(want) {
		t.Fatalf("glob manifest = %#v, want %#v", entries, want)
	}
	for i, e := range entries {
		if e.Rel != want[i].Rel || e.Kind != want[i].Kind || e.Target != want[i].Target || string(e.Data) != string(want[i].Data) {
			t.Fatalf("glob manifest[%d] = %#v, want %#v", i, e, want[i])
		}
	}
}
