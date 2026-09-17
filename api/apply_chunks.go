package api

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"

	"github.com/snonux/gonf/internal/privilege"
	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
)

// elevatedApplyRunner runs a privileged local apply chunk. Overridable in tests.
var elevatedApplyRunner = defaultElevatedApply

// elevatedApplyArgv builds the un-wrapped re-exec argv for the elevated
// child ("gonf apply [-n] <path>"). Split out from defaultElevatedApply so
// the dry-run propagation can be asserted by a unit test without spawning
// sudo/doas: dryRun must mirror resource.DryRun() at the call site (see
// remoteApplyCmd in internal/remote/remote.go for the equivalent remote-push
// argument), or the elevated child applies for real during a local
// "gonf -n" / "-dry-run" run.
func elevatedApplyArgv(exe, path string, dryRun bool) []string {
	if dryRun {
		return []string{exe, "apply", "-n", path}
	}
	return []string{exe, "apply", path}
}

func defaultElevatedApply(mode privilege.Mode, ops []plan.Op, planDir string) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("elevated apply: executable: %w", err)
	}
	raw, err := plan.EncodePlan(ops)
	if err != nil {
		return err
	}
	// Write temp plan next to blobs so apply can resolve blob paths.
	path := planDir + "/chunk-elevated.jsonl"
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return err
	}
	argv := elevatedApplyArgv(exe, path, resource.DryRun())
	argv, err = privilege.WrapArgv(mode, true, argv)
	if err != nil {
		return err
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	cmd.Stdin = bytes.NewReader(nil)
	return cmd.Run()
}

// ApplyChunks splits ops by elevate and applies each chunk: user chunks
// in-process, privileged chunks via sudo/doas re-exec (or in-process if
// root). Under resource.DryRun(), the elevated re-exec (built by
// defaultElevatedApply/elevatedApplyArgv) carries "-n" through to the child
// so a local "gonf -n"/"-dry-run" run previews the privileged chunk instead
// of actually mutating the host as root. A ValidateChunkDeps pre-flight runs
// first: a dep recorded in a later chunk (or dangling) fails before any
// chunk is applied, so a rejected plan mutates nothing.
func ApplyChunks(ops []plan.Op, planDir string, mode privilege.Mode) error {
	chunks := plan.SplitPrivilegeChunks(ops)
	if err := validateChunkDeps(chunks); err != nil {
		return err
	}
	for i, ch := range chunks {
		if !ch.Elevate {
			if err := ApplyPlan(ch.Ops, planDir); err != nil {
				return fmt.Errorf("chunk %d: %w", i, err)
			}
			continue
		}
		// LOCAL apply re-exec: this process's euid is the correct authority
		// here. Remote pushes must NEVER make this decision from the
		// controller's euid — see privilege.WrapApplyCmd's doc comment.
		if mode == privilege.None && os.Geteuid() == 0 {
			if err := ApplyPlan(ch.Ops, planDir); err != nil {
				return fmt.Errorf("chunk %d: %w", i, err)
			}
			continue
		}
		if err := elevatedApplyRunner(mode, ch.Ops, planDir); err != nil {
			return fmt.Errorf("chunk %d (elevated): %w", i, err)
		}
	}
	return nil
}

// validateChunkDeps runs the plan-level cross-chunk dependency pre-flight
// (plan.ValidateChunkDeps) over the split privilege chunks: forward
// cross-chunk and dangling deps fail before any chunk is applied or
// uploaded. Shared by ApplyChunks (api, local apply) and remote.PushChunks
// (internal/remote, SSH push).
func validateChunkDeps(chunks []plan.Chunk) error {
	bodies := make([][]plan.Op, len(chunks))
	for i, ch := range chunks {
		bodies[i] = ch.Ops
	}
	return plan.ValidateChunkDeps(bodies)
}
