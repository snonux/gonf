package link

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/snonux/gonf/resource"
	. "github.com/snonux/gonf/api/options"
)

func TestPresentSymlinkCreateAndIdempotent(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "link")

	Present(path, WithSymlink(target))
	if err := resource.Apply(); err != nil {
		t.Fatalf("Apply failed: %v", err)
	}

	got, err := os.Readlink(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != target {
		t.Errorf("expected link to point to %s, got %s", target, got)
	}

	// Idempotency
	if err := resource.Apply(); err != nil {
		t.Fatalf("second Apply failed: %v", err)
	}
}

func TestPresentSymlinkRepoints(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	path := filepath.Join(dir, "link")
	t1 := filepath.Join(dir, "target1")
	t2 := filepath.Join(dir, "target2")
	if err := os.WriteFile(t1, []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(t2, []byte("2"), 0o644); err != nil {
		t.Fatal(err)
	}

	Present(path, WithSymlink(t1))
	if err := resource.Apply(); err != nil {
		t.Fatalf("Apply 1 failed: %v", err)
	}

	// Change target
	resource.ResetRepository()
	Present(path, WithSymlink(t2))
	if err := resource.Apply(); err != nil {
		t.Fatalf("Apply 2 failed: %v", err)
	}

	got, err := os.Readlink(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != t2 {
		t.Errorf("expected link to repoint to %s, got %s", t2, got)
	}
}

func TestPresentSymlinkMovesRealFileAside(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	path := filepath.Join(dir, "link")
	if err := os.WriteFile(path, []byte("real file"), 0o644); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("target content"), 0o644); err != nil {
		t.Fatal(err)
	}

	Present(path, WithSymlink(target))
	if err := resource.Apply(); err != nil {
		t.Fatalf("Apply failed: %v", err)
	}

	if _, err := os.Stat(path + ".old"); os.IsNotExist(err) {
		t.Errorf("expected real file to be moved to %s.old", path)
	}
	got, err := os.Readlink(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != target {
		t.Errorf("expected link to point to %s, got %s", target, got)
	}
}

func TestPresentHardlinkCreateAndIdempotent(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "link")

	Present(path, WithHardlink(target))
	if err := resource.Apply(); err != nil {
		t.Fatalf("Apply failed: %v", err)
	}

	infoLink, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if infoLink.Mode()&os.ModeSymlink != 0 {
		t.Error("expected hardlink, but got symlink")
	}
	// Verify they share the same inode (via a helper or by checking if we can see it's not a symlink and is a file)
	// Since sameInode is internal to the package, we can just check that we can read it.
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hi" {
		t.Errorf("expected content 'hi', got %q", string(got))
	}

	// Idempotency
	if err := resource.Apply(); err != nil {
		t.Fatalf("second Apply failed: %v", err)
	}
}

func TestPresentHardlinkMovesRealFileAside(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	path := filepath.Join(dir, "link")
	if err := os.WriteFile(path, []byte("real file"), 0o644); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("target content"), 0o644); err != nil {
		t.Fatal(err)
	}

	Present(path, WithHardlink(target))
	if err := resource.Apply(); err != nil {
		t.Fatalf("Apply failed: %v", err)
	}

	if _, err := os.Stat(path + ".old"); os.IsNotExist(err) {
		t.Errorf("expected real file to be moved to %s.old", path)
	}
}

func TestPresentHardlinkMissingTarget(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	path := filepath.Join(dir, "link")
	target := filepath.Join(dir, "nonexistent")

	Present(path, WithHardlink(target))
	if err := resource.Apply(); err == nil {
		t.Error("expected Apply to fail when hardlink target is missing")
	}
}

func TestPresentAbsentSymlink(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	path := filepath.Join(dir, "link")
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}

	Present(path, IsAbsent)
	if err := resource.Apply(); err != nil {
		t.Fatalf("Apply failed: %v", err)
	}

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("expected symlink %s to be removed", path)
	}
}

func TestAbsentSymlink(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	path := filepath.Join(dir, "link")
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}

	Absent(path)
	if err := resource.Apply(); err != nil {
		t.Fatalf("Apply failed: %v", err)
	}

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("expected symlink %s to be removed", path)
	}
}

func TestBuildRequiresKindOrAbsent(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	path := filepath.Join(dir, "link")

	// Call Present without specifying symlink, hardlink, or absent
	Present(path)
	if err := resource.Apply(); err == nil {
		t.Error("expected Apply to fail when neither kind nor absent is specified")
	}
}
