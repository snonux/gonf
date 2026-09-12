package plan

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
)

// ApplyStagingRoot returns $TMPDIR/gonf-apply/<uid> (owner-only).
func ApplyStagingRoot() (string, error) {
	uid := "nouser"
	if u, err := user.Current(); err == nil && u.Uid != "" {
		uid = u.Uid
	} else {
		uid = strconv.Itoa(os.Getuid())
	}
	root := filepath.Join(os.TempDir(), "gonf-apply", uid)
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", fmt.Errorf("plan apply staging: %w", err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		return "", fmt.Errorf("plan apply staging chmod: %w", err)
	}
	return root, nil
}

// SweepApplyStaging removes all entries under the staging root (prior aborted runs).
func SweepApplyStaging(root string) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, e := range entries {
		path := filepath.Join(root, e.Name())
		if err := os.RemoveAll(path); err != nil {
			return fmt.Errorf("plan apply sweep %s: %w", path, err)
		}
	}
	return nil
}

// NewApplyRunDir sweeps leftovers then creates a fresh 0700 run directory.
func NewApplyRunDir() (dir string, cleanup func(), err error) {
	root, err := ApplyStagingRoot()
	if err != nil {
		return "", nil, err
	}
	if err := SweepApplyStaging(root); err != nil {
		return "", nil, err
	}
	dir, err = os.MkdirTemp(root, "run-*")
	if err != nil {
		return "", nil, err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		_ = os.RemoveAll(dir)
		return "", nil, err
	}
	cleanup = func() { _ = os.RemoveAll(dir) }
	return dir, cleanup, nil
}
