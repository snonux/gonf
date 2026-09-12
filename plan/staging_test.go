package plan

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSweepApplyStagingRemovesLeftovers(t *testing.T) {
	root, err := ApplyStagingRoot()
	if err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(root, "stale-run")
	if err := os.MkdirAll(stale, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stale, "blob"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir, cleanup, err := NewApplyRunDir()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale should be swept: %v", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("run dir mode %#o", info.Mode().Perm())
	}
}
