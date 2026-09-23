package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

// sigpipeHelperPlanEnv names the plan path TestSIGPIPEDispositionHelperProcess
// applies; sigpipeHelperArgsEnv carries the extra cliApply flags (space
// separated) to pass before that plan path.
const (
	sigpipeHelperPlanEnv = "GONF_TEST_SIGPIPE_HELPER_PLAN"
	sigpipeHelperArgsEnv = "GONF_TEST_SIGPIPE_HELPER_ARGS"
)

// sigpipeMarkerPrefix tags the one line TestSIGPIPEDispositionHelperProcess
// prints reporting this process's own SIGPIPE disposition, so its stdout can
// be picked out even though cliApplyFile/cliApply also print their own
// "applied ..." line to stdout.
const sigpipeMarkerPrefix = "SIGPIPE_IGNORED="

// TestSIGPIPEDispositionHelperProcess is not a test by itself (it skips
// unless sigpipeHelperPlanEnv is set): run as a subprocess by
// runSIGPIPEHelper, it runs the real cliApply entry point with the given
// args and then reports this PROCESS's own SIGPIPE disposition, read
// straight from /proc/self/status's SigIgn bitmask — the same measurement
// task 7d2's own review probe used — rather than trusting any internal
// bookkeeping. That is the only way to genuinely observe what
// signal.Ignore(syscall.SIGPIPE) did to this process, as opposed to merely
// confirming that cliApply's code path was reached.
func TestSIGPIPEDispositionHelperProcess(t *testing.T) {
	planPath := os.Getenv(sigpipeHelperPlanEnv)
	if planPath == "" {
		t.Skip("helper process only")
	}
	var args []string
	if raw := os.Getenv(sigpipeHelperArgsEnv); raw != "" {
		args = strings.Fields(raw)
	}
	args = append(args, planPath)
	code := cliApply(context.Background(), args)
	ignored, err := sigpipeIgnored()
	if err != nil {
		fmt.Fprintln(os.Stderr, "sigpipeIgnored:", err)
		os.Exit(3)
	}
	fmt.Println(sigpipeMarkerPrefix + strconv.FormatBool(ignored))
	os.Exit(code)
}

// sigpipeIgnored reports whether this process (Linux only) currently has
// SIGPIPE's disposition set to SIG_IGN, read from /proc/self/status's SigIgn
// hex bitmask: bit (signum-1) set means that signal is ignored (POSIX
// signal numbers are 1-based, so SIGPIPE=13 is bit index 12).
func sigpipeIgnored() (bool, error) {
	raw, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(string(raw), "\n") {
		rest, ok := strings.CutPrefix(line, "SigIgn:")
		if !ok {
			continue
		}
		mask, err := strconv.ParseUint(strings.TrimSpace(rest), 16, 64)
		if err != nil {
			return false, fmt.Errorf("parse SigIgn %q: %w", rest, err)
		}
		return mask&(1<<(uint(syscall.SIGPIPE)-1)) != 0, nil
	}
	return false, fmt.Errorf("SigIgn not found in /proc/self/status")
}

// runSIGPIPEHelper runs TestSIGPIPEDispositionHelperProcess as a real
// subprocess, applying a trivial touch plan through cliApply with the given
// extra args, and reports whether that process's SIGPIPE disposition was
// SIG_IGN once cliApply returned.
func runSIGPIPEHelper(t *testing.T, planPath string, args ...string) bool {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("SigIgn probe reads /proc/self/status (linux only)")
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestSIGPIPEDispositionHelperProcess$")
	cmd.Env = append(os.Environ(),
		sigpipeHelperPlanEnv+"="+planPath,
		sigpipeHelperArgsEnv+"="+strings.Join(args, " "))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("helper process failed: %v\noutput:\n%s", err, out)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), sigpipeMarkerPrefix); ok {
			ignored, err := strconv.ParseBool(rest)
			if err != nil {
				t.Fatalf("helper marker line %q: %v", line, err)
			}
			return ignored
		}
	}
	t.Fatalf("helper output missing %q marker:\n%s", sigpipeMarkerPrefix, out)
	return false
}

// TestSIGPIPENotIgnoredForPlainFileApply is task 7d2's core regression pin:
// an ordinary, non-relayed `gonf apply <plan.jsonl>` (no -cancel-pipe, no
// -relayed) must leave this process's own SIGPIPE disposition untouched.
// Before the fix, cliApply called ignoreSIGPIPEForRelayedChild
// unconditionally, on the wrong assumption that it is reached only as a
// relayed child's entry point; the review's own /proc/self/status probe
// measured the opposite directly: plain `gonf apply plan.jsonl` showed
// SIGPIPE ignored when it should not have been.
func TestSIGPIPENotIgnoredForPlainFileApply(t *testing.T) {
	root := t.TempDir()
	path := writeTouchPlan(t, root, "ok.jsonl", filepath.Join(root, "marker"))
	if runSIGPIPEHelper(t, path) {
		t.Fatal("SIGPIPE ignored for a plain, non-relayed `gonf apply <plan.jsonl>`")
	}
}

// TestSIGPIPEIgnoredForCancelPipe pins the local elevated re-exec shape:
// "-cancel-pipe" (set only by api.elevatedApplyArgv) must still ignore
// SIGPIPE, exactly as before task 7d2 — that process really is a relayed
// child (api.runElevatedCmd relays its output via logger.RunRelayed).
func TestSIGPIPEIgnoredForCancelPipe(t *testing.T) {
	root := t.TempDir()
	path := writeTouchPlan(t, root, "ok.jsonl", filepath.Join(root, "marker"))
	if !runSIGPIPEHelper(t, path, "-cancel-pipe") {
		t.Fatal("SIGPIPE not ignored for -cancel-pipe (the local elevated re-exec shape)")
	}
}

// TestSIGPIPEIgnoredForRelayed pins the new remote push/preview marker
// (internal/remote's remoteApplyCmd sets "-relayed" unconditionally on
// every remote apply command it builds, task 7d2): the receiving end of a
// push/preview over ssh must ignore SIGPIPE the same way, so a controller
// crash mid-relay does not SIGPIPE-kill it either.
func TestSIGPIPEIgnoredForRelayed(t *testing.T) {
	root := t.TempDir()
	path := writeTouchPlan(t, root, "ok.jsonl", filepath.Join(root, "marker"))
	if !runSIGPIPEHelper(t, path, "-relayed") {
		t.Fatal("SIGPIPE not ignored for -relayed (the push/preview relayed-child shape)")
	}
}

// TestSIGPIPENotIgnoredForStdinPipedPlan pins the other documented,
// non-relayed local shape: a manually piped `gonf apply -` (docs/plan.md's
// "apply file or GONF-PUSH/1 stdin"), with neither marker set, must also
// leave SIGPIPE alone.
func TestSIGPIPENotIgnoredForStdinPipedPlan(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("SigIgn probe reads /proc/self/status (linux only)")
	}
	root := t.TempDir()
	marker := filepath.Join(root, "marker")
	planPath := writeTouchPlan(t, root, "ok.jsonl", marker)
	raw, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestSIGPIPEDispositionHelperProcess$")
	cmd.Env = append(os.Environ(),
		sigpipeHelperPlanEnv+"=-",
		sigpipeHelperArgsEnv+"=")
	cmd.Stdin = strings.NewReader(string(raw))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("helper process failed: %v\noutput:\n%s", err, out)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), sigpipeMarkerPrefix); ok {
			ignored, err := strconv.ParseBool(rest)
			if err != nil {
				t.Fatalf("helper marker line %q: %v", line, err)
			}
			if ignored {
				t.Fatal("SIGPIPE ignored for a manually piped `gonf apply -`")
			}
			return
		}
	}
	t.Fatalf("helper output missing %q marker:\n%s", sigpipeMarkerPrefix, out)
}
