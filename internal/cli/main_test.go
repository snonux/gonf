package cli

import (
	"fmt"
	"os"
	"testing"

	"github.com/snonux/gonf/internal/testutil"
)

// TestMain resolves a symlinked TMPDIR before any test runs: tests here use
// t.TempDir() as `gonf plan -o` output dirs, which gonf refuses when a path
// component is a symlink (see testutil.ResolveTempDirEnv). Tests that need a
// symlinked TMPDIR (TestCLIWithSymlinkedTempDir) build their own symlink
// below the resolved dir, so it is the only symlink they exercise.
//
// It also points XDG_CONFIG_HOME at an empty directory for the whole
// package (isolateAmbientConfig): since task 5b2, a plain `gonf plan -o`
// of a sensitive plan seals by default when the operator recipients file
// ($XDG_CONFIG_HOME/gonf/recipients) exists, so a developer's own file
// must not change what the plaintext-path tests see. Tests that need a
// recipients file still create one below their own isolateXDGConfig dir.
func TestMain(m *testing.M) {
	cleanup, err := isolateAmbientConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	code := testutil.RunWithResolvedTempDir(m)
	cleanup()
	os.Exit(code)
}

// isolateAmbientConfig sets XDG_CONFIG_HOME to a fresh empty directory
// and returns its removal.
func isolateAmbientConfig() (func(), error) {
	dir, err := os.MkdirTemp("", "gonf-cli-xdg-*")
	if err != nil {
		return nil, fmt.Errorf("isolate XDG_CONFIG_HOME: %w", err)
	}
	if err := os.Setenv("XDG_CONFIG_HOME", dir); err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("isolate XDG_CONFIG_HOME: %w", err)
	}
	return func() { _ = os.RemoveAll(dir) }, nil
}
