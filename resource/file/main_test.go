package file

import (
	"os"
	"testing"

	"github.com/snonux/gonf/internal/testutil"
)

// TestMain resolves a symlinked TMPDIR before any test runs: tests here use
// t.TempDir() as validation-candidate parents, which gonf refuses when a path
// component is a symlink (see testutil.ResolveTempDirEnv).
func TestMain(m *testing.M) {
	os.Exit(testutil.RunWithResolvedTempDir(m))
}
