package plan

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// A symlink planted at the per-uid staging root is refused, and the
// directory it points to keeps its mode.
func TestApplyStagingRootRefusesSymlink(t *testing.T) {
	isolateStagingRoot(t)
	tmp := os.Getenv("TMPDIR")
	victim := filepath.Join(tmp, "victim")
	if err := os.Mkdir(victim, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(victim, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(tmp, "gonf-apply"), 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, filepath.Join(tmp, "gonf-apply", strconv.Itoa(os.Getuid()))); err != nil {
		t.Fatal(err)
	}
	if root, err := ApplyStagingRoot(); err == nil {
		t.Fatalf("symlinked staging root accepted: %s", root)
	}
	info, err := os.Stat(victim)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("symlink target mode changed to %o", info.Mode().Perm())
	}
}

// A shared root owned by another (non-root) user is not used: its owner could
// swap the per-uid directory despite the sticky bit.
func TestApplyStagingRootAvoidsForeignSharedRoot(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root to create a foreign-owned directory")
	}
	isolateStagingRoot(t)
	tmp := os.Getenv("TMPDIR")
	shared := filepath.Join(tmp, "gonf-apply")
	if err := os.Mkdir(shared, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(shared, 65534, 65534); err != nil {
		t.Fatal(err)
	}
	root, err := ApplyStagingRoot()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(tmp, "gonf-apply-"+strconv.Itoa(os.Getuid())); root != want {
		t.Fatalf("staging root = %s, want %s", root, want)
	}
}
