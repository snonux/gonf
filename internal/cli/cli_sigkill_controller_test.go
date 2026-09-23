package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/snonux/gonf/internal/logger"
	"github.com/snonux/gonf/plan"
)

// sigkillProbeOps mirrors the shape of the review's own probe for task lb2
// (RunRelayed relaying `sh -c 'sleep 1; echo step1; echo applied > marker'`,
// SIGKILLed 300ms in, which never wrote marker): a first Command op that
// touches started and then sleeps, so the outer test can kill the
// controller once it is known to be mid-apply, followed by a second Command
// op that only runs — and so only touches marker — if the child survives
// the write its own logger makes right before running it ("running
// Command[touch]: ...", resource/cmd/cmd.go's run) once that write lands on
// a relay pipe whose reader (the controller) is already gone.
func sigkillProbeOps(started, marker string) []plan.Op {
	return []plan.Op{
		{Op: plan.KindPlan, Version: plan.CurrentVersion, ID: "sigkill-probe"},
		{Op: plan.KindCommand, Bin: "sh", Args: []string{"-c", "touch '" + started + "'; sleep 1"}, ID: "Command[sleep]"},
		{Op: plan.KindCommand, Bin: "touch", Args: []string{marker}, ID: "Command[touch]"},
	}
}

// sigkillChildPlanEnv names the plan path TestSIGKillApplyChildHelperProcess
// applies.
const sigkillChildPlanEnv = "GONF_TEST_SIGKILL_CHILD_PLAN"

// TestSIGKillApplyChildHelperProcess is not a test by itself (it skips
// unless sigkillChildPlanEnv is set): run as a subprocess by
// TestSIGKillControllerHelperProcess, it applies the named plan through the
// real cliApply entry point, exactly as a local elevated sudo/doas re-exec
// or the receiving end of a push would, so it exercises the actual fix
// under test rather than a stand-in.
func TestSIGKillApplyChildHelperProcess(t *testing.T) {
	path := os.Getenv(sigkillChildPlanEnv)
	if path == "" {
		t.Skip("helper process only")
	}
	os.Exit(cliApply(context.Background(), []string{path}))
}

// sigkillControllerPlanEnv names the plan path
// TestSIGKillControllerHelperProcess passes to its own child helper.
const sigkillControllerPlanEnv = "GONF_TEST_SIGKILL_CONTROLLER_PLAN"

// TestSIGKillControllerHelperProcess is not a test by itself (it skips
// unless sigkillControllerPlanEnv is set): run as a subprocess by
// TestSIGKilledControllerChildSurvives, it plays the controller's part —
// starting the child helper above and relaying its stdout/stderr through
// logger.RunRelayed exactly as api.runElevatedCmd (local elevated apply)
// and internal/remote's push do — and then simply keeps running until the
// outer test SIGKILLs it, so the relay pipe's read end vanishes out from
// under the child mid-apply without this controller ever choosing to exit.
func TestSIGKillControllerHelperProcess(t *testing.T) {
	path := os.Getenv(sigkillControllerPlanEnv)
	if path == "" {
		t.Skip("helper process only")
	}
	child := exec.Command(os.Args[0], "-test.run=^TestSIGKillApplyChildHelperProcess$")
	child.Env = append(os.Environ(), sigkillChildPlanEnv+"="+path)
	_ = logger.RunRelayed(child, os.Stderr)
	// Reached only if the child (and the relay) finished before the outer
	// test's SIGKILL arrived; either way there is nothing left to do.
	os.Exit(0)
}

// TestSIGKilledControllerChildSurvives is task lb2's regression pin. An
// earlier review found that RunRelayed's "detached-cat hand-off" only
// covers a descendant outliving its own gonf's normal exit or context
// kill: if the CONTROLLER ITSELF is killed (SIGKILL, OOM, a crash) while a
// relayed local elevated apply or the remote end of a push is still
// running, the relay pipe's read end closes out from under it, and its next
// write used to raise SIGPIPE and kill it outright, mid-apply.
//
// Two real OS processes stand in for the controller and the relayed child
// (a third, this test, plays the outer operator/orchestrator that only the
// controller answers to): the controller relays the child through
// logger.RunRelayed exactly as api.runElevatedCmd and internal/remote's
// push do, and only the controller is SIGKILLed, once the child is known to
// be mid-apply (the "started" file). The child must still finish and touch
// marker — before task lb2's fix it did not, because the child's own
// pre-command log write ("running Command[touch]: ...") landed on the
// now-orphaned pipe and killed it before the touch ever ran.
func TestSIGKilledControllerChildSurvives(t *testing.T) {
	root := t.TempDir()
	started, marker := filepath.Join(root, "started"), filepath.Join(root, "marker")
	planPath := writePlanFile(t, root, sigkillProbeOps(started, marker))

	controller := exec.Command(os.Args[0], "-test.run=^TestSIGKillControllerHelperProcess$")
	controller.Env = append(os.Environ(), sigkillControllerPlanEnv+"="+planPath)
	controller.Stderr = os.Stderr
	if err := controller.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = controller.Process.Kill()
		_ = controller.Wait()
	})

	whenStarted(t, started, func() { _ = controller.Process.Kill() })
	waitForFile(t, marker)
}
