package dir

import (
	"os"
	"path/filepath"
	"testing"

	. "github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
)

// applyEnsureDirOp lowers an EnsureDir draft for path and applies it through
// the ensure_dir handler, as a destination does.
func applyEnsureDirOp(t *testing.T, path string, opts ...DirOption) error {
	t.Helper()
	draft, err := EnsurePlanDraft(path, opts...)
	if err != nil {
		t.Fatal(err)
	}
	op, err := ensureDirHandler{}.ToOp(draft)
	if err != nil {
		t.Fatal(err)
	}
	return ensureDirHandler{}.Apply(op, plan.ApplyContext{})
}

// TestEnsureDirOpCreatesOnlyWhenMissing pins the documented ensure_dir
// contract ("destination creates it only if missing"), matching direct-mode
// api.EnsureDir: a missing directory is created with the recorded mode, an
// existing one keeps its mode (the draft records build()'s 0750 default even
// without WithMode) and a symlink to a directory counts as present instead of
// being refused.
func TestEnsureDirOpCreatesOnlyWhenMissing(t *testing.T) {
	t.Run("missing is created", func(t *testing.T) {
		resource.ResetRepository()
		path := filepath.Join(t.TempDir(), "a", "b")
		if err := applyEnsureDirOp(t, path, WithMode(0o700)); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if !info.IsDir() || info.Mode().Perm() != 0o700 {
			t.Fatalf("created %v, want a 0700 directory", info.Mode())
		}
	})

	t.Run("existing keeps its mode", func(t *testing.T) {
		resource.ResetRepository()
		resource.ResetReport()
		path := filepath.Join(t.TempDir(), "bin")
		if err := os.Mkdir(path, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := applyEnsureDirOp(t, path); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o755 {
			t.Fatalf("mode = %v, want the existing 0755 untouched", info.Mode().Perm())
		}
		if resource.AnyChanged(resource.FormatID("Directory", path)) {
			t.Fatal("an existing directory was reported changed")
		}
	})

	t.Run("symlink to a directory counts as present", func(t *testing.T) {
		resource.ResetRepository()
		base := t.TempDir()
		real := filepath.Join(base, "real")
		if err := os.Mkdir(real, 0o755); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(base, "link")
		if err := os.Symlink(real, path); err != nil {
			t.Fatal(err)
		}
		if err := applyEnsureDirOp(t, path); err != nil {
			t.Fatalf("ensure_dir refused a symlink to a directory: %v", err)
		}
		if info, err := os.Lstat(path); err != nil || info.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("symlink was replaced or removed: %v, %v", info, err)
		}
	})

	t.Run("non-directory is refused", func(t *testing.T) {
		resource.ResetRepository()
		path := filepath.Join(t.TempDir(), "file")
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := applyEnsureDirOp(t, path); err == nil {
			t.Fatal("ensure_dir accepted a regular file at its path")
		}
	})
}
