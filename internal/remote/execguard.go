package remote

import (
	"fmt"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"testing"
)

// gonfModule is gonf's own module path. The guard only applies to gonf's own
// test binaries: a project that imports gonf (a client config repo) may have
// integration tests that push to real hosts on purpose, and cannot reach
// internal/remote's seams to fake them.
const gonfModule = "github.com/snonux/gonf"

// inGonfTestBinary reports whether this process is a `go test` binary of a
// gonf package; computed once.
var inGonfTestBinary = sync.OnceValue(func() bool {
	if !testing.Testing() {
		return false
	}
	info, ok := debug.ReadBuildInfo()
	return ok && info.Main.Path == gonfModule
})

// networkExecBinaries are the argv[0] basenames that reach a remote host.
// Only these are refused: the runners are also exercised with harmless local
// commands (e.g. "sleep" in the context-kill tests), which must keep working.
var networkExecBinaries = map[string]bool{"ssh": true, "scp": true}

// refuseNetworkExecInTests panics when a test binary is about to exec a real
// ssh or scp. It is called by every production runner that starts one:
// defaultSSHRunner (SSHRunner), defaultSSHCaptureExec (sshCaptureExec) and
// defaultSCPRunner (Pusher.SCPRunner). Outside gonf's own test binaries
// (a normal run, or the tests of a project that imports gonf) it is a no-op.
//
// Why a guard in the exec path rather than a TestMain in each package: the
// seams are swapped from test files in api/, internal/cli/,
// internal/orchestrate/ and here, and a forgotten fake used to fail
// silently — sshCapture turns an ssh failure into empty output and the
// release-version probe only logs its errors, so tests passed while running
// ssh against their fake *.example hosts (task x72). Checking here covers
// every importing package with no exported test-only API.
//
// It panics instead of returning an error for the same reason: an error
// from these runners is frequently swallowed or expected by the caller,
// while a panic always fails the test and names the argv that escaped.
// Tests that really need ssh (the remote_smoke build tag) exec it through
// their own os/exec calls or a custom SSHRunner, never through these
// defaults.
func refuseNetworkExecInTests(argv []string) {
	if len(argv) == 0 || !inGonfTestBinary() {
		return
	}
	if !networkExecBinaries[filepath.Base(argv[0])] {
		return
	}
	// The runners are often called from errgroup goroutines, where a panic
	// kills the test binary without a "--- FAIL" line; dumping every
	// goroutine includes the running test's own, which names it.
	debug.SetTraceback("all")
	panic(fmt.Sprintf("gonf/internal/remote: un-faked %s reached inside a test binary: %q; "+
		"fake remote.SSHRunner and use remote.AssumeRemotePlanCurrent (push) or "+
		"remote.AssumeRemoteGonfCurrent (preview), or give the Pusher fake probes",
		argv[0], strings.Join(argv, " ")))
}
