package safepath

import (
	"os"
	"testing"

	"github.com/snonux/gonf/internal/testutil"
)

// TestMain resolves a symlinked TMPDIR before any test runs: walks here start
// at "/" and descend through t.TempDir()'s components, which Walk refuses when
// one is a symlink (see testutil.ResolveTempDirEnv).
func TestMain(m *testing.M) {
	os.Exit(testutil.RunWithResolvedTempDir(m))
}
