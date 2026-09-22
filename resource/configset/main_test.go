package configset

import (
	"os"
	"testing"

	"github.com/snonux/gonf/internal/testutil"
)

// TestMain resolves a symlinked TMPDIR before any test runs: tests here use
// t.TempDir() as the live and staging directories of config sets, which are
// opened component by component and refused when one is a symlink (see
// testutil.ResolveTempDirEnv).
func TestMain(m *testing.M) {
	os.Exit(testutil.RunWithResolvedTempDir(m))
}
