package cli

import (
	"os"
	"testing"

	"github.com/snonux/gonf/internal/testutil"
)

// TestMain resolves a symlinked TMPDIR before any test runs: tests here use
// t.TempDir() as `gonf plan -o` output dirs, which gonf refuses when a path
// component is a symlink (see testutil.ResolveTempDirEnv). Tests that need a
// symlinked TMPDIR (TestCLIWithSymlinkedTempDir) build their own symlink
// below the resolved dir, so it is the only symlink they exercise.
func TestMain(m *testing.M) {
	os.Exit(testutil.RunWithResolvedTempDir(m))
}
