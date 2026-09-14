package dir

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	. "github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/resource"
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

	Present(path, IsAbsent)
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

	Present(path, IsAbsent, WithPrune)
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

	Absent(path, WithPrune)
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

		Present(dst, WithSource(src), WithPrune)
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
	Present(dst, WithSource(src), WithPrune)
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

func TestSourceGlobInstallsMatchingFiles(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "dst")
	if err := os.Mkdir(src, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a.rb", "b.rb", "skip.txt"} {
		if err := os.WriteFile(filepath.Join(src, name), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	Present(dst, WithSourceGlob(filepath.Join(src, "*.rb")), WithFileMode(0o640))
	if err := resource.Apply(); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	for _, name := range []string{"a.rb", "b.rb"} {
		got, err := os.ReadFile(filepath.Join(dst, name))
		if err != nil {
			t.Fatalf("missing %s: %v", name, err)
		}
		if string(got) != name {
			t.Fatalf("%s content = %q", name, got)
		}
	}
	if _, err := os.Stat(filepath.Join(dst, "skip.txt")); !os.IsNotExist(err) {
		t.Fatal("skip.txt should not have been installed")
	}

	// Idempotent
	resource.ResetRepository()
	Present(dst, WithSourceGlob(filepath.Join(src, "*.rb")), WithFileMode(0o640))
	if err := resource.Apply(); err != nil {
		t.Fatalf("second Apply: %v", err)
	}
}

func TestSourceGlobPrune(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "dst")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "keep.rb"), []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dst, "keep.rb"), []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dst, "old.rb"), []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dst, "subdir"), 0o755); err != nil {
		t.Fatal(err)
	}

	Present(dst, WithSourceGlob(filepath.Join(src, "*.rb")), WithPrune)
	if err := resource.Apply(); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dst, "old.rb")); !os.IsNotExist(err) {
		t.Fatal("old.rb should have been pruned")
	}
	if _, err := os.Stat(filepath.Join(dst, "keep.rb")); err != nil {
		t.Fatal("keep.rb should remain")
	}
	if _, err := os.Stat(filepath.Join(dst, "subdir")); err != nil {
		t.Fatal("subdir should not be pruned")
	}
}

// TestSourceTreePruneDryRunKeepsStaleFiles guards against data loss: a
// dry-run apply (gonf -n) of a Dir with WithSource+WithPrune must only
// preview the prune (StatusWouldChange note), never delete stale destination
// files.
func TestSourceTreePruneDryRunKeepsStaleFiles(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "dst")

	if err := os.Mkdir(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "keep.txt"), []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dst, "keep.txt"), []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(dst, "stale.txt")
	staleContent := "precious stale content"
	if err := os.WriteFile(stale, []byte(staleContent), 0o644); err != nil {
		t.Fatal(err)
	}

	resource.SetDryRun(true)
	t.Cleanup(func() { resource.SetDryRun(false) })

	Present(dst, WithSource(src), WithPrune)
	if err := resource.Apply(); err != nil {
		t.Fatalf("Apply failed: %v", err)
	}

	got, err := os.ReadFile(stale)
	if err != nil {
		t.Fatalf("dry-run prune deleted %s: %v", stale, err)
	}
	if string(got) != staleContent {
		t.Errorf("stale.txt content = %q, want %q", got, staleContent)
	}
	gotKeep, err := os.ReadFile(filepath.Join(dst, "keep.txt"))
	if err != nil {
		t.Fatalf("keep.txt should remain after dry-run prune: %v", err)
	}
	if string(gotKeep) != "keep" {
		t.Errorf("keep.txt content = %q, want %q", gotKeep, "keep")
	}

	var buf bytes.Buffer
	resource.PrintSummary(&buf)
	if !strings.Contains(buf.String(), "would-change File["+stale+"]") {
		t.Errorf("expected a would-change note for %s, summary:\n%s", stale, buf.String())
	}
}

// TestAbsentDoesNotMutateCallerOptionSlice guards against 100 Go Mistakes
// #25: Absent used to append IsAbsent onto the caller-owned variadic slice,
// writing into the spare capacity of a reusable option list and silently
// turning later Present calls built from the same backing array into
// deletions.
func TestAbsentDoesNotMutateCallerOptionSlice(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	gone := filepath.Join(dir, "gone")
	keep := filepath.Join(dir, "keep")
	if err := os.Mkdir(gone, 0o755); err != nil {
		t.Fatal(err)
	}

	// Reusable option list with spare capacity (len 2, cap 8). Sub-slices
	// share its backing array.
	base := make([]Option, 2, 8)
	base[0] = WithMode(0o755)
	base[1] = WithPrune
	before := make([]Option, len(base))
	copy(before, base)

	Absent(gone, base[:1]...)
	Present(keep, base[:2]...)

	// (a) The caller-owned backing array must be unchanged.
	for i := range base {
		if reflect.ValueOf(base[i]).Pointer() != reflect.ValueOf(before[i]).Pointer() {
			t.Fatalf("caller option slice mutated at index %d: IsAbsent was injected into the caller's backing array", i)
		}
	}

	// (b) The Present resource built from the same backing array must not
	// have become absent.
	if err := resource.Apply(); err != nil {
		t.Fatalf("Apply failed: %v", err)
	}
	info, err := os.Stat(keep)
	if err != nil || !info.IsDir() {
		t.Fatalf("expected %s to still exist (Present must not be absent): %v", keep, err)
	}
	if _, err := os.Stat(gone); !os.IsNotExist(err) {
		t.Errorf("expected %s to be removed", gone)
	}
}
