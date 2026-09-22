package api

import "os"

// tempPlanDir creates a private (0700) temporary plan directory in $TMPDIR
// named after pattern (os.MkdirTemp) for Run and Apply, and returns it with
// the function that removes it. The caller defers remove.
//
// The directory holds packaged copies of the sources (possibly rendered
// secrets) while task bodies and resources run. gonf library code never ends
// the process (DSL misuse is a returned declaration error, internal/declerr),
// so the caller's deferred remove runs on every return path. Removal is
// idempotent. A process killed by a signal nothing handles, by SIGKILL or by
// a crash leaves the directory behind until the operating system cleans
// $TMPDIR.
func tempPlanDir(pattern string) (dir string, remove func(), err error) {
	dir, err = os.MkdirTemp("", pattern)
	if err != nil {
		return "", nil, err
	}
	return dir, func() { _ = os.RemoveAll(dir) }, nil
}
