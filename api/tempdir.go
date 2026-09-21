package api

import (
	"os"

	"github.com/snonux/gonf/internal/logger"
)

// tempPlanDir creates a private (0700) temporary plan directory in $TMPDIR
// named after pattern (os.MkdirTemp) for Run and Apply, and returns it with
// the function that removes it. The caller defers remove.
//
// The directory holds packaged copies of the sources (possibly rendered
// secrets) while task bodies and resources run, and a fail-fast
// logger.Fatal ends the process with os.Exit, which skips deferred calls. So
// the removal is also registered with logger.OnFatal, as the logger requires
// of code that keeps such copies on disk; remove unregisters that hook again,
// so a finished Run or Apply leaves no hook behind. Removal is idempotent: the
// hook and the deferred call may both run, and the second finds nothing to do.
//
// The hook is cleanup, not a barrier. When Fatal is called from the goroutine
// that runs Run or Apply (a task body, the usual case), that goroutine is
// inside Fatal, so nothing writes to the directory while the hook removes it.
// When Fatal is called from ANOTHER goroutine, Run or Apply keeps running
// while the hook runs and until os.Exit: a blob write in that window
// recreates the directory (the $TMPDIR store creates its root when missing)
// and is left behind. A process killed by a signal nothing handles, by
// SIGKILL or by a crash also leaves the directory behind until the operating
// system cleans $TMPDIR.
func tempPlanDir(pattern string) (dir string, remove func(), err error) {
	dir, err = os.MkdirTemp("", pattern)
	if err != nil {
		return "", nil, err
	}
	removeDir := func() { _ = os.RemoveAll(dir) }
	unregister := onFatal(removeDir)
	return dir, func() {
		unregister()
		removeDir()
	}, nil
}

// onFatal is logger.OnFatal. It is a variable only so a test can observe the
// registrations and check that every one is unregistered again
// (TestTempPlanDirLeavesNoFatalHook).
var onFatal = logger.OnFatal
