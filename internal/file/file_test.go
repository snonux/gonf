package file

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGetChecksum(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")

	if err := os.WriteFile(path, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	checksum := getChecksum(path)
	if checksum == [32]byte{} {
		t.Error("expected non-zero checksum")
	}

	zeroChecksum := getChecksum(filepath.Join(dir, "no-such-file"))
	var expectedZero [32]byte
	if zeroChecksum != expectedZero {
		t.Errorf("expected zero checksum for missing file, got %x", zeroChecksum)
	}
}

func TestWriteTmpFile(t *testing.T) {
	dir := t.TempDir()
	tmpPath := filepath.Join(dir, "test.tmp")
	content := []byte("temp content")

	if err := writeTmpFile(tmpPath, content, 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := os.ReadFile(tmpPath)
	if err != nil {
		t.Fatalf("reading tmp file: %v", err)
	}
	if string(got) != string(content) {
		t.Errorf("expected %q, got %q", content, got)
	}
}

func TestUpdateFromTmpChecksumChanged(t *testing.T) {
	dir := t.TempDir()
	tmpPath := filepath.Join(dir, "test.tmp")
	path := filepath.Join(dir, "test.txt")

	if err := os.WriteFile(tmpPath, []byte("new content"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := updateFromTmp(tmpPath, path, true); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, err := os.Stat(tmpPath); err == nil {
		t.Error("tmp file should have been removed")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading target file: %v", err)
	}
	if string(got) != "new content" {
		t.Errorf("expected 'new content', got %q", got)
	}
}

func TestUpdateFromTmpChecksumUnchanged(t *testing.T) {
	dir := t.TempDir()
	tmpPath := filepath.Join(dir, "test.tmp")
	path := filepath.Join(dir, "test.txt")

	if err := os.WriteFile(tmpPath, []byte("same"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("same"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := updateFromTmp(tmpPath, path, false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, err := os.Stat(tmpPath); err == nil {
		t.Error("tmp file should have been removed")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading target file: %v", err)
	}
	if string(got) != "same" {
		t.Errorf("expected 'same', got %q", got)
	}
}

func TestHaveStringCreateNewFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "new.txt")

	Have(path, WithContent("hello world"))

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading file: %v", err)
	}
	if string(got) != "hello world" {
		t.Errorf("expected 'hello world', got %q", got)
	}
}

func TestHaveMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mode.txt")
	mode := os.FileMode(0o600)

	Have(path, WithContent("mode test"), WithMode(mode))

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// Mask to check only permission bits
	if info.Mode().Perm() != mode {
		t.Errorf("expected mode %v, got %v", mode, info.Mode().Perm())
	}
}

func TestHaveSourceFile(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join("..", "..", "assets", "testfiles", "test.txt")
	targetPath := filepath.Join(dir, "target.txt")

	Have(targetPath, WithSource(sourcePath))

	got, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatalf("reading file: %v", err)
	}
	expected, _ := os.ReadFile(sourcePath)
	if string(got) != string(expected) {
		t.Errorf("expected %q, got %q", string(expected), string(got))
	}
}

func TestHaveTemplateFile(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join("..", "..", "assets", "testfiles", "test.tmpl")
	targetPath := filepath.Join(dir, "target.conf")

	Have(targetPath, WithSource(sourcePath))

	got, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatalf("reading file: %v", err)
	}

	expectedParam := sourcePath
	if !strings.Contains(string(got), expectedParam) {
		t.Errorf("expected content to contain Param %q, got %q", expectedParam, string(got))
	}
}

func TestHaveDirectoryCreate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "nested")

	Have(path, IsDirectory())

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if !info.IsDir() {
		t.Errorf("expected a directory at %s", path)
	}
	if info.Mode().Perm() != 0o750 {
		t.Errorf("expected default dir mode 0750, got %v", info.Mode().Perm())
	}
}

func TestHaveDirectoryIdempotentWithMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "d")

	// Call the resource logic directly to exercise idempotency without the
	// one-per-process resource registry rejecting a duplicate registration.
	f1 := &File{path: path, mode: 0o755}
	if err := f1.haveDirectory(); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	f2 := &File{path: path, mode: 0o700}
	if err := f2.haveDirectory(); err != nil {
		t.Fatalf("second apply: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Errorf("expected mode 0700 enforced, got %v", info.Mode().Perm())
	}
}

func TestHaveDirectoryFailsWhenFileExists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "afile")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := have(path, IsDirectory()); err == nil {
		t.Error("expected error when a regular file is in the way of a directory")
	}
}

func TestHaveSymlinkCreateAndIdempotent(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.txt")
	link := filepath.Join(dir, "link.txt")
	if err := os.WriteFile(target, []byte("t"), 0o644); err != nil {
		t.Fatal(err)
	}

	Have(link, IsSymlink(target))
	got, err := os.Readlink(link)
	if err != nil {
		t.Fatalf("readlink: %v", err)
	}
	if got != target {
		t.Errorf("expected link -> %s, got %s", target, got)
	}

	// Re-applying the same link should be a no-op (tested directly to avoid the
	// one-per-process resource registry rejecting a duplicate registration).
	f := &File{path: link, symlink: true, symlinkTarget: target}
	if err := f.haveSymlink(); err != nil {
		t.Fatalf("idempotent apply: %v", err)
	}
}

func TestHaveSymlinkRepoints(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "old.txt")
	newT := filepath.Join(dir, "new.txt")
	link := filepath.Join(dir, "link")
	for _, p := range []string{old, newT} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(old, link); err != nil {
		t.Fatal(err)
	}

	Have(link, IsSymlink(newT))
	got, err := os.Readlink(link)
	if err != nil {
		t.Fatal(err)
	}
	if got != newT {
		t.Errorf("expected repoint to %s, got %s", newT, got)
	}
}

func TestHaveSymlinkMovesRealFileAside(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.txt")
	link := filepath.Join(dir, "real")
	if err := os.WriteFile(target, []byte("t"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(link, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}

	Have(link, IsSymlink(target))

	got, err := os.Readlink(link)
	if err != nil {
		t.Fatalf("expected %s to be a symlink: %v", link, err)
	}
	if got != target {
		t.Errorf("expected link -> %s, got %s", target, got)
	}
	if data, err := os.ReadFile(link + ".old"); err != nil || string(data) != "original" {
		t.Errorf("expected original content preserved in %s.old, got %q err %v", link, string(data), err)
	}
}

func TestHaveAbsent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gone.txt")
	if err := os.WriteFile(path, []byte("bye"), 0o644); err != nil {
		t.Fatal(err)
	}

	Have(path, IsAbsent())
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("expected %s to be removed", path)
	}

	// Idempotent: removing a missing file is not an error (direct call to avoid
	// duplicate registration in the one-per-process registry).
	f := &File{path: path}
	if err := f.haveAbsent(); err != nil {
		t.Fatalf("absent on missing file: %v", err)
	}
}

func TestHaveHardlinkCreateAndIdempotent(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.txt")
	link := filepath.Join(dir, "link.txt")
	if err := os.WriteFile(target, []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}

	Have(link, IsHardlink(target))

	ti, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	li, err := os.Stat(link)
	if err != nil {
		t.Fatal(err)
	}
	if !sameInode(ti, li) {
		t.Errorf("expected %s and %s to share an inode", link, target)
	}

	// Re-applying the same link should be a no-op (direct call to avoid the
	// one-per-process resource registry rejecting a duplicate registration).
	f := &File{path: link, hardlink: true, hardlinkTarget: target}
	if err := f.haveHardlink(); err != nil {
		t.Fatalf("idempotent apply: %v", err)
	}
}

func TestHaveHardlinkMovesRealFileAside(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.txt")
	link := filepath.Join(dir, "real")
	if err := os.WriteFile(target, []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(link, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}

	Have(link, IsHardlink(target))

	ti, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	li, err := os.Stat(link)
	if err != nil {
		t.Fatal(err)
	}
	if !sameInode(ti, li) {
		t.Errorf("expected %s to be hardlinked to %s", link, target)
	}
	if data, err := os.ReadFile(link + ".old"); err != nil || string(data) != "original" {
		t.Errorf("expected original content preserved in %s.old, got %q err %v", link, string(data), err)
	}
}

func TestHaveHardlinkMissingTarget(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(dir, "link")
	if _, err := have(link, IsHardlink(filepath.Join(dir, "nope"))); err == nil {
		t.Error("expected error when hardlink target does not exist")
	}
}

func TestHaveAbsentNonEmptyDirWithoutPruneFails(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "d")
	if err := os.MkdirAll(filepath.Join(target, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}

	if _, err := have(target, IsAbsent()); err == nil {
		t.Error("expected error removing a non-empty directory without PruneDirectory()")
	}
	if _, err := os.Stat(target); err != nil {
		t.Errorf("expected %s to still exist, got %v", target, err)
	}
}

func TestHaveAbsentPruneDirectoryRecursive(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "d")
	if err := os.MkdirAll(filepath.Join(target, "sub", "deep"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "sub", "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	Have(target, IsAbsent(), PruneDirectory())
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Errorf("expected %s to be removed recursively", target)
	}

	// Idempotent: removing a missing tree is not an error.
	f := &File{path: target, absent: true, pruneDirectory: true}
	if err := f.haveAbsent(); err != nil {
		t.Fatalf("prune on missing tree: %v", err)
	}
}
