package dir

import (
	"os"
	"path/filepath"
	"testing"

	"codeberg.org/snonux/gonf/internal/resource"
	. "codeberg.org/snonux/gonf/api/options"
)

func TestPresentDirectoryCreate(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	path := filepath.Join(dir, "newdir")

	Present(path)
	if err := resource.Apply(); err != nil {
		t.Fatalf("Apply failed: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() {
		t.Errorf("expected %s to be a directory", path)
	}
}

func TestPresentDirectoryIdempotentWithMode(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	path := filepath.Join(dir, "modedir")
	mode := os.FileMode(0o700)

	Present(path, WithMode(mode))
	if err := resource.Apply(); err != nil {
		t.Fatalf("Apply failed: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != mode {
		t.Errorf("expected mode %v, got %v", mode, info.Mode().Perm())
	}

	// Idempotency check
	if err := resource.Apply(); err != nil {
		t.Fatalf("second Apply failed: %v", err)
	}
}

func TestPresentDirectoryFailsWhenFileExists(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	path := filepath.Join(dir, "myfile")
	if err := os.WriteFile(path, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	Present(path)
	if err := resource.Apply(); err == nil {
		t.Error("expected Apply to fail when a file exists at the directory path")
	}
}

func TestPresentAbsentNonEmptyDirWithoutPruneFails(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	path := filepath.Join(dir, "nonempty")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "file"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}

	Present(path, IsAbsent())
	if err := resource.Apply(); err == nil {
		t.Error("expected Apply to fail when removing non-empty directory without prune")
	}
}

func TestPresentAbsentPruneDirectoryRecursive(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	path := filepath.Join(dir, "pruneme")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "file"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}

	Present(path, IsAbsent(), WithPrune())
	if err := resource.Apply(); err != nil {
		t.Fatalf("Apply failed: %v", err)
	}

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("expected %s to be removed recursively", path)
	}
}

func TestAbsentPruneDirectoryRecursive(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	path := filepath.Join(dir, "pruneme2")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "file"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}

	Absent(path, WithPrune())
	if err := resource.Apply(); err != nil {
		t.Fatalf("Apply failed: %v", err)
	}

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("expected %s to be removed recursively", path)
	}
}

func TestPresentDirectoryWithSource(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "dst")

	if err := os.Mkdir(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(src, "subdir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "f1"), []byte("content1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "subdir", "f2"), []byte("content2"), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Run("Create", func(t *testing.T) {
		resource.ResetRepository()
		Present(dst, WithSource(src))
		if err := resource.Apply(); err != nil {
			t.Fatalf("Apply failed: %v", err)
		}

		if _, err := os.Stat(filepath.Join(dst, "f1")); err != nil {
			t.Errorf("missing file f1: %v", err)
		}
		if _, err := os.Stat(filepath.Join(dst, "subdir", "f2")); err != nil {
			t.Errorf("missing file f2: %v", err)
		}
	})

	t.Run("Prune", func(t *testing.T) {
		resource.ResetRepository()
		// Add extra file to dst
		extra := filepath.Join(dst, "extra")
		if err := os.WriteFile(extra, []byte("extra"), 0o644); err != nil {
			t.Fatal(err)
		}

		Present(dst, WithSource(src), WithPrune())
		if err := resource.Apply(); err != nil {
			t.Fatalf("Apply failed: %v", err)
		}

		if _, err := os.Stat(extra); !os.IsNotExist(err) {
			t.Errorf("expected %s to be pruned", extra)
		}
	})
}

func TestSourceCopyUsesFileModeDefaultNotDirMode(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "dst")

	if err := os.Mkdir(src, 0o755); err != nil {
		t.Fatal(err)
	}
	f1 := filepath.Join(src, "f1")
	if err := os.WriteFile(f1, []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Use non-default dir mode to prove files don't use it
	Present(dst, WithSource(src), WithMode(0o700))
	if err := resource.Apply(); err != nil {
		t.Fatalf("Apply failed: %v", err)
	}

	info, err := os.Stat(filepath.Join(dst, "f1"))
	if err != nil {
		t.Fatal(err)
	}
	// Default fileMode is 0o640
	if info.Mode().Perm() != 0o640 {
		t.Errorf("expected file mode 0o640, got %v", info.Mode().Perm())
	}
}

func TestSourceCopyRespectsExplicitWithFileMode(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "dst")

	if err := os.Mkdir(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "f1"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}

	explicitMode := os.FileMode(0o600)
	Present(dst, WithSource(src), WithFileMode(explicitMode))
	if err := resource.Apply(); err != nil {
		t.Fatalf("Apply failed: %v", err)
	}

	info, err := os.Stat(filepath.Join(dst, "f1"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != explicitMode {
		t.Errorf("expected file mode %v, got %v", explicitMode, info.Mode().Perm())
	}
}

func TestSourceCopyStripsTmplSuffixOnCopiedFile(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "dst")

	if err := os.Mkdir(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "foo.conf.tmpl"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	Present(dst, WithSource(src))
	if err := resource.Apply(); err != nil {
		t.Fatalf("Apply failed: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dst, "foo.conf")); err != nil {
		t.Errorf("expected stripped file foo.conf to exist: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "foo.conf.tmpl")); !os.IsNotExist(err) {
		t.Errorf("expected non-stripped file foo.conf.tmpl to NOT exist")
	}
}

func TestSourceCopyWithPruneKeepsTemplatedFile(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "dst")

	if err := os.Mkdir(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "foo.conf.tmpl"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	// First apply to create the file
	Present(dst, WithSource(src))
	if err := resource.Apply(); err != nil {
		t.Fatalf("Apply failed: %v", err)
	}

	// Now apply with prune
	resource.ResetRepository()
	Present(dst, WithSource(src), WithPrune())
	if err := resource.Apply(); err != nil {
		t.Fatalf("Apply failed: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dst, "foo.conf")); err != nil {
		t.Errorf("expected foo.conf to be kept during pruning: %v", err)
	}
}

func TestSourceCopyParamMatchesSingleFilePath(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "dst")

	if err := os.Mkdir(src, 0o755); err != nil {
		t.Fatal(err)
	}
	f1 := filepath.Join(src, "foo.conf.tmpl")
	if err := os.WriteFile(f1, []byte("path is {{.Param}}"), 0o644); err != nil {
		t.Fatal(err)
	}

	Present(dst, WithSource(src))
	if err := resource.Apply(); err != nil {
		t.Fatalf("Apply failed: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(dst, "foo.conf"))
	if err != nil {
		t.Fatal(err)
	}
	expected := "path is " + f1
	if string(got) != expected {
		t.Errorf("expected %q, got %q", expected, string(got))
	}
}

func TestSourceCopyRecreatesSymlinkNotContent(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "dst")

	if err := os.Mkdir(src, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(src, "realfile")
	if err := os.WriteFile(target, []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	linkPath := filepath.Join(src, "link")
	if err := os.Symlink(target, linkPath); err != nil {
		t.Fatal(err)
	}

	Present(dst, WithSource(src))
	if err := resource.Apply(); err != nil {
		t.Fatalf("Apply failed: %v", err)
	}

	dstLink := filepath.Join(dst, "link")
	info, err := os.Lstat(dstLink)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("expected %s to be a symlink", dstLink)
	}
}
