package testutil

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// ResolveTempDirEnv points TMPDIR at the symlink-resolved form of
// os.TempDir(), so every later t.TempDir() and os.MkdirTemp("", ...) in the
// process returns a path without symlinked components.
//
// gonf deliberately refuses symlinked components in operator plan dirs and
// validation-candidate parents. When the system temp dir is reached through a
// symlink (the macOS default, /var -> /private/var, or a symlinked TMPDIR on
// Linux) tests that use t.TempDir() as such a directory would otherwise fail
// for an environmental reason. Resolving the temp dir once here keeps those
// production refusals intact; tests that exercise them create their own
// symlinks below the resolved dir.
func ResolveTempDirEnv() error {
	resolved, err := filepath.EvalSymlinks(os.TempDir())
	if err != nil {
		return fmt.Errorf("resolve temp dir: %w", err)
	}
	if err := os.Setenv("TMPDIR", resolved); err != nil {
		return fmt.Errorf("set TMPDIR: %w", err)
	}
	return nil
}

// RunWithResolvedTempDir is a TestMain body: it calls ResolveTempDirEnv and
// then runs the package's tests, returning the exit code for os.Exit. A
// failure to resolve the temp dir is reported on stderr and returns 1.
func RunWithResolvedTempDir(m *testing.M) int {
	if err := ResolveTempDirEnv(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return m.Run()
}
