package link

import (
	"os"
	"path/filepath"
	"testing"

	. "codeberg.org/snonux/gonf/internal/resource/opt"
)

func TestHaveSymlinkCreateAndIdempotent(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.txt")
	link := filepath.Join(dir, "link.txt")
	if err := os.WriteFile(target, []byte("t"), 0o644); err != nil {
		t.Fatal(err)
	}

	Have(link, WithSymlink(target))
	got, err := os.Readlink(link)
	if err != nil {
		t.Fatalf("readlink: %v", err)
	}
	if got != target {
		t.Errorf("expected link -> %s, got %s", target, got)
	}

	// Re-applying the same link should be a no-op (tested directly to avoid the
	// one-per-process resource registry rejecting a duplicate registration).
	l := &Link{path: link, kind: symlinkKind, target: target}
	if err := ensureSymlink(l); err != nil {
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

	Have(link, WithSymlink(newT))
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

	Have(link, WithSymlink(target))

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

func TestHaveHardlinkCreateAndIdempotent(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.txt")
	link := filepath.Join(dir, "link.txt")
	if err := os.WriteFile(target, []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}

	Have(link, WithHardlink(target))

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
	l := &Link{path: link, kind: hardlinkKind, target: target}
	if err := ensureHardlink(l); err != nil {
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

	Have(link, WithHardlink(target))

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
	if err := Ensure(link, WithHardlink(filepath.Join(dir, "nope"))); err == nil {
		t.Error("expected error when hardlink target does not exist")
	}
}

func TestHaveAbsentSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.txt")
	link := filepath.Join(dir, "link.txt")
	if err := os.WriteFile(target, []byte("t"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	Have(link, IsAbsent())
	if _, err := os.Lstat(link); !os.IsNotExist(err) {
		t.Errorf("expected %s to be removed", link)
	}

	// Idempotent: removing a missing link is not an error (direct call to
	// avoid duplicate registration in the one-per-process registry).
	if err := ensureAbsent(link); err != nil {
		t.Fatalf("absent on missing link: %v", err)
	}
}

func TestAbsentSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.txt")
	link := filepath.Join(dir, "link.txt")
	if err := os.WriteFile(target, []byte("t"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	Absent(link)
	if _, err := os.Lstat(link); !os.IsNotExist(err) {
		t.Errorf("expected %s to be removed", link)
	}

	// Idempotent: removing a missing link is not an error (direct call to
	// avoid duplicate registration in the one-per-process registry).
	if err := ensureAbsent(link); err != nil {
		t.Fatalf("absent on missing link: %v", err)
	}
}

func TestHaveAbsentWithoutKindRegistersGenericLink(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "whatever")

	l := build(path, IsAbsent())
	if got := l.resourceType(); got != "Link" {
		t.Errorf("expected generic resource type %q, got %q", "Link", got)
	}
}

func TestBuildRequiresKindOrAbsent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nope")
	if err := Ensure(path); err == nil {
		t.Error("expected error when neither IsSymlink, IsHardlink, nor IsAbsent is set")
	}
}
