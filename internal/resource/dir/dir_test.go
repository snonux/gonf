package dir

import (
	"os"
	"path/filepath"
	"testing"

	"codeberg.org/snonux/gonf/internal/resource/file"
)

func TestHaveDirectoryCreate(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "sub", "nested")

	Have(path)

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
	tmp := t.TempDir()
	path := filepath.Join(tmp, "d")

	// Call ensureDirectorySelf directly to exercise idempotency without the
	// one-per-process resource registry rejecting a duplicate registration.
	d1 := &Dir{path: path, mode: 0o755}
	if err := ensureDirectorySelf(d1); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	d2 := &Dir{path: path, mode: 0o700}
	if err := ensureDirectorySelf(d2); err != nil {
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
	tmp := t.TempDir()
	path := filepath.Join(tmp, "afile")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := Ensure(path); err == nil {
		t.Error("expected error when a regular file is in the way of a directory")
	}
}

func TestHaveAbsentNonEmptyDirWithoutPruneFails(t *testing.T) {
	tmp := t.TempDir()
	target := filepath.Join(tmp, "d")
	if err := os.MkdirAll(filepath.Join(target, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := Ensure(target, IsAbsent()); err == nil {
		t.Error("expected error removing a non-empty directory without WithPrune()")
	}
	if _, err := os.Stat(target); err != nil {
		t.Errorf("expected %s to still exist, got %v", target, err)
	}
}

func TestHaveAbsentPruneDirectoryRecursive(t *testing.T) {
	tmp := t.TempDir()
	target := filepath.Join(tmp, "d")
	if err := os.MkdirAll(filepath.Join(target, "sub", "deep"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "sub", "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	Have(target, IsAbsent(), WithPrune())
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Errorf("expected %s to be removed recursively", target)
	}

	// Idempotent: removing a missing tree is not an error.
	d := &Dir{path: target, absent: true, prune: true}
	if err := ensureAbsent(d); err != nil {
		t.Fatalf("prune on missing tree: %v", err)
	}
}

func TestHaveDirectoryWithSource(t *testing.T) {
	t.Run("recursive copy", func(t *testing.T) {
		tmp := t.TempDir()
		src := t.TempDir()
		dst := filepath.Join(tmp, "dst")

		srcFile := filepath.Join(src, "file.txt")
		if err := os.WriteFile(srcFile, []byte("hello"), 0o644); err != nil {
			t.Fatal(err)
		}
		srcSub := filepath.Join(src, "sub")
		if err := os.MkdirAll(srcSub, 0o755); err != nil {
			t.Fatal(err)
		}
		srcSubFile := filepath.Join(srcSub, "subfile.txt")
		if err := os.WriteFile(srcSubFile, []byte("sub hello"), 0o644); err != nil {
			t.Fatal(err)
		}

		Have(dst, WithSource(src))

		if data, err := os.ReadFile(filepath.Join(dst, "file.txt")); err != nil || string(data) != "hello" {
			t.Errorf("expected 'hello' at %s, got %q err %v", filepath.Join(dst, "file.txt"), string(data), err)
		}
		if data, err := os.ReadFile(filepath.Join(dst, "sub", "subfile.txt")); err != nil || string(data) != "sub hello" {
			t.Errorf("expected 'sub hello' at %s, got %q err %v", filepath.Join(dst, "sub", "subfile.txt"), string(data), err)
		}
	})

	t.Run("pruning", func(t *testing.T) {
		tmp := t.TempDir()
		src := t.TempDir()
		dst := filepath.Join(tmp, "dst")

		srcFile := filepath.Join(src, "file.txt")
		if err := os.WriteFile(srcFile, []byte("hello"), 0o644); err != nil {
			t.Fatal(err)
		}

		if err := os.MkdirAll(dst, 0o755); err != nil {
			t.Fatal(err)
		}
		extra := filepath.Join(dst, "extra.txt")
		if err := os.WriteFile(extra, []byte("extra"), 0o644); err != nil {
			t.Fatal(err)
		}
		extraSub := filepath.Join(dst, "extra-sub")
		if err := os.MkdirAll(extraSub, 0o755); err != nil {
			t.Fatal(err)
		}

		Have(dst, WithSource(src), WithPrune())

		if _, err := os.Stat(extra); !os.IsNotExist(err) {
			t.Errorf("expected %s to be pruned", extra)
		}
		if _, err := os.Stat(extraSub); !os.IsNotExist(err) {
			t.Errorf("expected %s to be pruned", extraSub)
		}
		if data, err := os.ReadFile(filepath.Join(dst, "file.txt")); err != nil || string(data) != "hello" {
			t.Errorf("expected 'hello' at %s, got %q err %v", filepath.Join(dst, "file.txt"), string(data), err)
		}
	})
}

func TestSourceCopyUsesFileModeDefaultNotDirMode(t *testing.T) {
	tmp := t.TempDir()
	src := t.TempDir()
	dst := filepath.Join(tmp, "dst")

	if err := os.WriteFile(filepath.Join(src, "file.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	Have(dst, WithSource(src))

	dirInfo, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if dirInfo.Mode().Perm() != 0o750 {
		t.Errorf("expected dir mode 0750, got %v", dirInfo.Mode().Perm())
	}

	fileInfo, err := os.Stat(filepath.Join(dst, "file.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if fileInfo.Mode().Perm() != 0o640 {
		t.Errorf("expected copied file mode 0640 (not the directory's 0750), got %v", fileInfo.Mode().Perm())
	}
}

func TestSourceCopyRespectsExplicitWithFileMode(t *testing.T) {
	tmp := t.TempDir()
	src := t.TempDir()
	dst := filepath.Join(tmp, "dst")

	if err := os.WriteFile(filepath.Join(src, "file.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	Have(dst, WithSource(src), WithMode(0o755), WithFileMode(0o600))

	dirInfo, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if dirInfo.Mode().Perm() != 0o755 {
		t.Errorf("expected dir mode 0755, got %v", dirInfo.Mode().Perm())
	}

	fileInfo, err := os.Stat(filepath.Join(dst, "file.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if fileInfo.Mode().Perm() != 0o600 {
		t.Errorf("expected copied file mode 0600, got %v", fileInfo.Mode().Perm())
	}
}

func TestSourceCopyStripsTmplSuffixOnCopiedFile(t *testing.T) {
	tmp := t.TempDir()
	src := t.TempDir()
	dst := filepath.Join(tmp, "dst")

	if err := os.WriteFile(filepath.Join(src, "foo.conf.tmpl"), []byte("hello {{.Param}}"), 0o644); err != nil {
		t.Fatal(err)
	}

	Have(dst, WithSource(src))

	if _, err := os.Stat(filepath.Join(dst, "foo.conf")); err != nil {
		t.Errorf("expected de-suffixed foo.conf to exist: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "foo.conf.tmpl")); !os.IsNotExist(err) {
		t.Errorf("expected foo.conf.tmpl to NOT exist on disk")
	}
}

func TestSourceCopyWithPruneKeepsTemplatedFile(t *testing.T) {
	tmp := t.TempDir()
	src := t.TempDir()
	dst := filepath.Join(tmp, "dst")

	if err := os.WriteFile(filepath.Join(src, "foo.conf.tmpl"), []byte("hello {{.Param}}"), 0o644); err != nil {
		t.Fatal(err)
	}

	// WithPrune reconciles the destination against the source on every
	// apply; a de-suffixed templated file must not be pruned just because
	// its own name has no direct match in the source tree.
	Have(dst, WithSource(src), WithPrune())

	if _, err := os.Stat(filepath.Join(dst, "foo.conf")); err != nil {
		t.Errorf("expected foo.conf to survive pruning, got %v", err)
	}
}

func TestSourceCopyParamMatchesSingleFilePath(t *testing.T) {
	tmp := t.TempDir()
	src := t.TempDir()
	dst := filepath.Join(tmp, "dst")
	sourcePath := filepath.Join(src, "foo.conf.tmpl")
	if err := os.WriteFile(sourcePath, []byte("{{.Param}}"), 0o644); err != nil {
		t.Fatal(err)
	}

	Have(dst, WithSource(src))
	viaDir, err := os.ReadFile(filepath.Join(dst, "foo.conf"))
	if err != nil {
		t.Fatal(err)
	}

	singleTarget := filepath.Join(tmp, "single.conf")
	if err := file.Ensure(singleTarget, file.WithSource(sourcePath)); err != nil {
		t.Fatal(err)
	}
	viaFile, err := os.ReadFile(singleTarget)
	if err != nil {
		t.Fatal(err)
	}

	if string(viaDir) != string(viaFile) {
		t.Errorf("expected identical .Param rendering via both paths, dir-copy=%q file-direct=%q", viaDir, viaFile)
	}
}

func TestSourceCopyRecreatesSymlinkNotContent(t *testing.T) {
	tmp := t.TempDir()
	src := t.TempDir()
	dst := filepath.Join(tmp, "dst")

	if err := os.WriteFile(filepath.Join(src, "target.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("target.txt", filepath.Join(src, "link.txt")); err != nil {
		t.Fatal(err)
	}

	Have(dst, WithSource(src))

	linkPath := filepath.Join(dst, "link.txt")
	info, err := os.Lstat(linkPath)
	if err != nil {
		t.Fatalf("lstat: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("expected %s to be a symlink, got mode %v", linkPath, info.Mode())
	}
	got, err := os.Readlink(linkPath)
	if err != nil {
		t.Fatalf("readlink: %v", err)
	}
	if got != "target.txt" {
		t.Errorf("expected symlink target %q, got %q", "target.txt", got)
	}
}
