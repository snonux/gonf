package api

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/snonux/gonf/internal/privilege"
	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
)

// DefaultChunkTimeout bounds one elevated (sudo/doas re-exec) local apply
// chunk when the ctx passed to ApplyChunksContext carries no deadline of its
// own. A privileged chunk can legitimately run for a while (package installs,
// service restarts across many resources), so the default is generous;
// mirrors remote.DefaultHostTimeout's role for the SSH push path. A chunk
// that outlives it is killed and reported as an apply failure instead of
// hanging the whole process forever (the resilience gap this constant
// closes: previously the re-exec'd child had no timeout and no context at
// all).
const DefaultChunkTimeout = 10 * time.Minute

// elevatedApplyRunner runs a privileged local apply chunk. Overridable in tests.
var elevatedApplyRunner = defaultElevatedApply

// elevatedApplyArgv builds the un-wrapped re-exec argv for the elevated
// child ("gonf [-profile=<override>] apply [-n] <path>"). Split out from
// defaultElevatedApply so the dry-run and profile-override propagation can be
// asserted by a unit test without spawning sudo/doas: dryRun must mirror
// resource.DryRun() at the call site (see remoteApplyCmd in
// internal/remote/remote.go for the equivalent remote-push argument), or the
// elevated child applies for real during a local "gonf -n" / "-dry-run" run.
//
// profileOverride must mirror api.ProfileOverride() at the call site, or the
// elevated child re-derives its profile from the host (DetectFacts) instead
// of inheriting the parent's "-profile" override — causing when_begin
// profile predicates to evaluate inconsistently between the unprivileged
// chunks (evaluated in-process, with the override) and the privileged chunk
// (re-exec'd child, without it). "-profile" is a GLOBAL flag parsed by the
// top-level flag.FlagSet in internal/cli/cli.go's CLI(), not by cliApply's
// "apply" subcommand flag set, so it must precede "apply" in argv. A CLI flag
// is used rather than an environment variable because sudo's env_reset (the
// default) strips inherited env vars before the child even starts.
func elevatedApplyArgv(exe, path string, dryRun bool, profileOverride string) []string {
	argv := []string{exe}
	if profileOverride != "" {
		argv = append(argv, "-profile="+profileOverride)
	}
	argv = append(argv, "apply")
	if dryRun {
		argv = append(argv, "-n")
	}
	return append(argv, path)
}

// defaultElevatedApply runs the elevated re-exec under ctx: canceling ctx
// (e.g. the CLI's SIGINT/SIGTERM context) kills the in-flight sudo/doas
// child. When ctx carries no deadline of its own, DefaultChunkTimeout is
// applied so a wedged privileged command (a hung package manager, a
// systemctl call waiting on a broken unit) cannot block the whole apply
// forever — previously this used plain exec.Command with no context at all.
func defaultElevatedApply(ctx context.Context, mode privilege.Mode, ops []plan.Op, planDir string) error {
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
	argv := elevatedApplyArgv(exe, path, resource.DryRun(), ProfileOverride())
	argv, err = privilege.WrapArgv(mode, true, argv)
	if err != nil {
		return err
	}
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, DefaultChunkTimeout)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	cmd.Stdin = bytes.NewReader(nil)
	err = cmd.Run()
	if err != nil && ctx.Err() != nil {
		return fmt.Errorf("%w (elevated apply killed by context: %v)", ctx.Err(), err)
	}
	return err
}

// ApplyChunks splits ops by elevate and applies each chunk: user chunks
// in-process, privileged chunks via sudo/doas re-exec (or in-process if
// root). Under resource.DryRun(), the elevated re-exec (built by
// defaultElevatedApply/elevatedApplyArgv) carries "-n" through to the child
// so a local "gonf -n"/"-dry-run" run previews the privileged chunk instead
// of actually mutating the host as root. Likewise, an active CLI
// "-profile=<override>" is re-propagated to the elevated child so
// when_begin{fact:profile,...} guards evaluate identically in the
// unprivileged (in-process) and privileged (re-exec'd) chunks of the same
// plan, instead of the child re-detecting the profile from the actual host.
// A plan.ValidateChunks pre-flight runs first: a dep recorded in a later chunk
// (or dangling) fails before any chunk is applied, so a rejected plan
// mutates nothing.
//
// ApplyChunks itself runs without a caller-supplied context (equivalent to
// ApplyChunksContext(context.Background(), ...)): this is the signature
// nested api.Run calls (task bodies invoking Run("othertask")) and any
// existing caller already depend on, so it is kept exactly as-is for API
// stability. New callers that can supply a cancelable/timeout-bound context
// (the CLI entry point, in particular) should use ApplyChunksContext
// instead — its elevated chunks still get DefaultChunkTimeout even when the
// given ctx has no deadline, so an unprivileged in-process chunk is the only
// part of a local apply this context does not currently reach (see
// internal/exec's own process-wide default timeout for why that gap is
// still safe: every resource backend's actual command execution is bounded
// there regardless of ctx threading).
func ApplyChunks(ops []plan.Op, planDir string, mode privilege.Mode) error {
	return ApplyChunksContext(context.Background(), ops, planDir, mode)
}

// ApplyChunksContext is ApplyChunks bounded/cancelable by ctx: canceling ctx
// (e.g. the CLI's SIGINT/SIGTERM context) kills an in-flight elevated
// sudo/doas re-exec. See ApplyChunks's doc comment for why ApplyChunks itself
// keeps the old context.Background() behavior instead of taking ctx directly.
func ApplyChunksContext(ctx context.Context, ops []plan.Op, planDir string, mode privilege.Mode) error {
	chunks := plan.SplitPrivilegeChunks(ops)
	if err := plan.ValidateChunks(chunks); err != nil {
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
		if err := elevatedApplyRunner(ctx, mode, ch.Ops, planDir); err != nil {
			return fmt.Errorf("chunk %d (elevated): %w", i, err)
		}
	}
	return nil
}
