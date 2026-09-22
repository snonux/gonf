package plan

import (
	"os"
	"testing"

	"github.com/snonux/gonf/internal/testutil"
)

// TestMain resolves a symlinked TMPDIR before any test runs (for both the
// plan and plan_test packages of this test binary): tests here use
// t.TempDir() as plan dirs, which gonf refuses when a path component is a
// symlink (see testutil.ResolveTempDirEnv).
func TestMain(m *testing.M) {
	os.Exit(testutil.RunWithResolvedTempDir(m))
}
