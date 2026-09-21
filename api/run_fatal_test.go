package api

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/snonux/gonf/internal/logger"
	"golang.org/x/sys/unix"
)

// These tests pin that the temporary plan directories of Run
// ($TMPDIR/gonf-plan-*) and Apply ($TMPDIR/gonf-apply-*) do not outlive a
// fail-fast logger.Fatal: both hold packaged copies of the sources (possibly
// rendered secrets), and logger.Fatal exits with os.Exit, which skips the
// deferred os.RemoveAll. Each runs the fatal path in a child process (the
// helper tests below) with its own empty $TMPDIR, which must be empty again
// when the child has exited.

const (
	tempFatalEnv    = "GONF_TEMP_FATAL_HELPER" // "run" or "apply"; also marks the child
	tempFatalBigEnv = "GONF_TEMP_FATAL_BIG"    // large source file to package
)

// TestTempDirFatalHelperProcess is the child of TestRunFatalRemovesTempPlanDir
// and TestApplyFatalRemovesTempPlanDir, not a test of its own.
func TestTempDirFatalHelperProcess(t *testing.T) {
	mode := os.Getenv(tempFatalEnv)
	if mode == "" {
		t.Skip("helper process only")
	}
	big := os.Getenv(tempFatalBigEnv)
	dst := filepath.Join(filepath.Dir(big), "dst-big")
	switch mode {
	case "run":
		// A task body packages a large blob into Run's temp dir, then hits a
		// fail-fast logger.Fatal.
		Task("fatal_after_blob", "", func() {
			InstallFile(dst, big)
			logger.Fatal("fail-fast DSL misuse after packaging a blob")
		})
		_ = Run("fatal_after_blob")
	case "apply":
		// Apply packages the big file into its temp dir, then blocks, in Go
		// code, reading the second source: a FIFO nobody ever writes. No
		// external process is started, so nothing outlives the child.
		// logger.Fatal fires from another goroutine once the first blob is on
		// disk, while Apply is still running (and not writing: it is blocked).
		fifo := filepath.Join(filepath.Dir(big), "never-written.fifo")
		if err := unix.Mkfifo(fifo, 0o600); err != nil {
			t.Fatal(err)
		}
		// Drafts are packaged in resource-ID order: "z-blocked" after "dst-big".
		InstallFile(dst, big)
		InstallFile(filepath.Join(filepath.Dir(big), "z-blocked"), fifo)
		go fatalOnceBlobPackaged("gonf-apply-*")
		_ = Apply()
	}
	t.Fatal("logger.Fatal did not end the process")
}

// fatalOnceBlobPackaged waits until a blob exists below a $TMPDIR directory
// matching pattern (by its final name: not the dot-prefixed temp file of the
// atomic write) and then calls logger.Fatal (from this goroutine, while the
// caller is still inside Apply).
func fatalOnceBlobPackaged(pattern string) {
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if blobs, _ := filepath.Glob(filepath.Join(os.TempDir(), pattern, "blobs", "[^.]*")); len(blobs) > 0 {
			logger.Fatal("fail-fast exit while Apply runs")
		}
	}
	logger.Fatal("no blob was packaged below %s", pattern)
}

// runTempFatalHelper runs the helper in mode with an empty private $TMPDIR and
// returns that directory once the child exited through logger.Fatal.
func runTempFatalHelper(t *testing.T, mode, wantMsg string) string {
	t.Helper()
	tmp := t.TempDir()
	src := newStagedSources(t)
	cmd := exec.Command(os.Args[0], "-test.run=^TestTempDirFatalHelperProcess$")
	cmd.Env = append(os.Environ(), tempFatalEnv+"="+mode, tempFatalBigEnv+"="+src.big, "TMPDIR="+tmp)
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 {
		t.Fatalf("helper: %v, want exit status 1 from logger.Fatal; output:\n%s", err, out)
	}
	if !strings.Contains(string(out), wantMsg) {
		t.Fatalf("helper did not reach logger.Fatal (%q); output:\n%s", wantMsg, out)
	}
	return tmp
}

// TestRunFatalRemovesTempPlanDir: a task body that ends the process with
// logger.Fatal after a large InstallFile was packaged used to leave
// $TMPDIR/gonf-plan-*/blobs/... behind. Run registers the removal with
// logger.OnFatal, so the child's $TMPDIR is empty afterwards.
func TestRunFatalRemovesTempPlanDir(t *testing.T) {
	assertNoStagingLeft(t, runTempFatalHelper(t, "run", "fail-fast DSL misuse after packaging a blob"))
}

// TestApplyFatalRemovesTempPlanDir is the same for Apply's
// $TMPDIR/gonf-apply-* directory: logger.Fatal while Apply runs (after the
// blob was packaged) must not leave it behind.
func TestApplyFatalRemovesTempPlanDir(t *testing.T) {
	assertNoStagingLeft(t, runTempFatalHelper(t, "apply", "fail-fast exit while Apply runs"))
}

// TestTempPlanDirLeavesNoFatalHook: a Run and an Apply that return normally
// (success, and a Run refused after its temp dir was made) unregister the
// fatal hook they registered, so no hook for an already removed directory is
// left behind for a later Fatal. The onFatal seam counts live registrations.
func TestTempPlanDirLeavesNoFatalHook(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)
	t.Setenv("TMPDIR", t.TempDir())
	dst := t.TempDir()
	live, registered := 0, 0
	prev := onFatal
	onFatal = func(fn func()) func() {
		unregister := prev(fn)
		live++
		registered++
		return func() { live--; unregister() }
	}
	t.Cleanup(func() { onFatal = prev })

	Task("hook_ok", "", func() { Dir(filepath.Join(dst, "run")) })
	Task("hook_refused", "", refusedRecordCauses()[0].bad)
	if err := Run("hook_ok"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if err := Run("hook_refused"); err == nil {
		t.Fatal("Run of a refused record = nil, want a refusal")
	}
	ResetForTest()
	Dir(filepath.Join(dst, "apply"))
	if err := Apply(); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if registered != 3 || live != 0 {
		t.Fatalf("fatal hooks: %d registered, %d still live; want 3 and 0", registered, live)
	}
}
