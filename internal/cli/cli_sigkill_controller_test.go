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
		{Op: plan.KindCommand, ID: "Command[sleep]", Payload: plan.CommandPayload{Bin: "sh", Args: []string{"-c", "touch '" + started + "'; sleep 1"}}},
		{Op: plan.KindCommand, ID: "Command[touch]", Payload: plan.CommandPayload{Bin: "touch", Args: []string{marker}}},
	}
}

// sigkillChildPlanEnv names the plan path TestSIGKillApplyChildHelperProcess
// applies.
const sigkillChildPlanEnv = "GONF_TEST_SIGKILL_CHILD_PLAN"

// TestSIGKillApplyChildHelperProcess is not a test by itself (it skips
// unless sigkillChildPlanEnv is set): run as a subprocess by
// TestSIGKillControllerHelperProcess, it applies the named plan through the
// real cliApply entry point, WITH "-cancel-pipe" — exactly as the local
// elevated sudo/doas re-exec's actual argv shape (api.elevatedApplyArgv
// always sets it), which is also what makes cliApply ignore SIGPIPE at all
// (task 7d2 scoped that to only a genuine relayed child; before that fix
// this helper's plain, markerless invocation happened to still ignore
// SIGPIPE, which is why leaving the flag out here did not, on its own,
// break this specific regression test even though the doc comment's claim
// of exercising "the elevated shape" was never actually true — task 6d2).
// Its stdin is not itself a controlled pipe here (this test's own concern
// is SIGPIPE-survival, not the cancel-byte protocol — see
// TestSIGKilledCancelPipeChildIgnoresBareEOF below for that, with a real
// pipe deliberately held open, unwritten, by the controller); it inherits
// whatever stdin the test binary has, which reads as an immediate, bare EOF
// (no byte) and, per task 6d2's fix, must NOT cancel the apply.
func TestSIGKillApplyChildHelperProcess(t *testing.T) {
	path := os.Getenv(sigkillChildPlanEnv)
	if path == "" {
		t.Skip("helper process only")
	}
	os.Exit(cliApply(context.Background(), []string{"-cancel-pipe", path}))
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

// sigkillCancelPipeChildPlanEnv names the plan path
// TestSIGKillCancelPipeChildHelperProcess applies.
const sigkillCancelPipeChildPlanEnv = "GONF_TEST_SIGKILL_CANCELPIPE_CHILD_PLAN"

// TestSIGKillCancelPipeChildHelperProcess is not a test by itself: run as a
// subprocess by TestSIGKillCancelPipeControllerHelperProcess below, with its
// stdin wired to the READ end of a real os.Pipe() that only its controller
// process (not this one) holds the write end of — exactly the shape
// api.wireElevatedCancelPipe gives the real elevated child, and exactly what
// task 6d2's fix (internal/cli's watchCancelPipe) is required to get right:
// a bare close of that write end, with no cancel byte ever written to it,
// must NOT cancel this apply.
func TestSIGKillCancelPipeChildHelperProcess(t *testing.T) {
	path := os.Getenv(sigkillCancelPipeChildPlanEnv)
	if path == "" {
		t.Skip("helper process only")
	}
	os.Exit(cliApply(context.Background(), []string{"-cancel-pipe", path}))
}

// sigkillCancelPipeControllerPlanEnv names the plan path
// TestSIGKillCancelPipeControllerHelperProcess passes to its own child
// helper.
const sigkillCancelPipeControllerPlanEnv = "GONF_TEST_SIGKILL_CANCELPIPE_CONTROLLER_PLAN"

// TestSIGKillCancelPipeControllerHelperProcess is not a test by itself: run
// as a subprocess by TestSIGKilledCancelPipeChildIgnoresBareEOF, it plays
// the controller's part for the real cancel-pipe protocol, mirroring
// api.wireElevatedCancelPipe/api.runElevatedCmd exactly: it creates a real
// os.Pipe(), wires the read end as the child's stdin, relays the child
// through logger.RunRelayed (as api.runElevatedCmd does), and then simply
// keeps running — holding the write end open, UNWRITTEN — until the outer
// test SIGKILLs it. So the write end closes only because this controller
// died, never because anything wrote the one-byte cancel signal to it
// (api.cancelPipeByte). If the child (task 6d2's fix) is doing its job,
// that bare close (bare EOF, no byte ever read) must NOT cancel it.
func TestSIGKillCancelPipeControllerHelperProcess(t *testing.T) {
	path := os.Getenv(sigkillCancelPipeControllerPlanEnv)
	if path == "" {
		t.Skip("helper process only")
	}
	r, w, err := os.Pipe()
	if err != nil {
		os.Exit(1)
	}
	// Never written to: this controller dies (SIGKILLed by the outer test)
	// before ever choosing to cancel, exactly like a crashed/OOM-killed
	// controller in production.
	defer func() { _ = w.Close() }()
	child := exec.Command(os.Args[0], "-test.run=^TestSIGKillCancelPipeChildHelperProcess$")
	child.Env = append(os.Environ(), sigkillCancelPipeChildPlanEnv+"="+path)
	child.Stdin = r
	_ = logger.RunRelayed(child, os.Stderr)
	// Reached only if the child (and the relay) finished before the outer
	// test's SIGKILL arrived; either way there is nothing left to do.
	os.Exit(0)
}

// TestSIGKilledCancelPipeChildIgnoresBareEOF is task 6d2's regression pin,
// exercising the actual cancel-pipe protocol end to end (unlike
// TestSIGKilledControllerChildSurvives above, which only pins SIGPIPE
// survival and never wires a real, controller-held cancel pipe — see task
// 6d2's own finding about that test's stale "elevated shape" claim). A real
// os.Pipe(), wired exactly as api.wireElevatedCancelPipe wires it, has its
// write end closed only because the CONTROLLER holding it was SIGKILLed —
// never because anything wrote the one-byte cancel signal to it. Before
// task 6d2's fix, watchCancelPipe treated any close (bare EOF included) as
// a cancel; the elevated child would then wrongly stop the in-flight
// privileged command mid-apply, defeating task lb2's whole point (the child
// must keep applying even if the controller dies). marker must still get
// written: the plan must run to completion, uncanceled.
func TestSIGKilledCancelPipeChildIgnoresBareEOF(t *testing.T) {
	root := t.TempDir()
	started, marker := filepath.Join(root, "started"), filepath.Join(root, "marker")
	planPath := writePlanFile(t, root, sigkillProbeOps(started, marker))

	controller := exec.Command(os.Args[0], "-test.run=^TestSIGKillCancelPipeControllerHelperProcess$")
	controller.Env = append(os.Environ(), sigkillCancelPipeControllerPlanEnv+"="+planPath)
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
