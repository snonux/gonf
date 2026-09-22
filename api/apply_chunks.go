package api

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/snonux/gonf/internal/clihost"
	"github.com/snonux/gonf/internal/logger"
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

// processEUID is os.Geteuid; a variable so tests can pin the mode-none
// refusal (and the in-process root branch) whatever user runs them.
var processEUID = os.Geteuid

// errNoCLIHost refuses the elevated re-exec in a process that does not run
// gonf's CLI (see internal/clihost): re-executing such a program as
// `<binary> apply <chunk>` would run its own main again as root.
var errNoCLIHost = errors.New("privileged apply re-executes this binary as `<binary> apply <chunk>`, " +
	"which only works in a program whose main calls cli.CLI(); run the tasks through the gonf CLI " +
	"(e.g. `gonf <task>`), or drop WithElevate/Privileged()")

// chunkLabel names chunk i in a chunk's apply error.
type chunkLabel func(i int, ch plan.Chunk) string

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
	cmd.Stdin = bytes.NewReader(nil)
	// The elevated child has no secret registry of its own: its log lines
	// and summary reach the operator's terminal through the controller's
	// redactor, line by line. A descendant still holding the relay pipe after
	// the child exited or was killed (an orphaned root gonf apply once the
	// context killed sudo) delays the return by at most logger.RelayWaitDelay;
	// then its output is handed, unredacted, to a detached cat writing to
	// stderr, so it keeps a reader even after gonf exits and is never killed
	// by SIGPIPE mid-apply (logger.RunRelayed).
	err = logger.RunRelayed(cmd, os.Stderr)
	if err != nil && ctx.Err() != nil {
		return fmt.Errorf("%w (elevated apply killed by context: %v)", ctx.Err(), err)
	}
	return err
}

// preflightElevation refuses, before ANY chunk is applied, a plan whose
// elevated chunks could not run: with privilege mode none a non-root process
// has no way to elevate (the re-exec would fail only after the unprivileged
// chunks before it had mutated the host), and the sudo/doas re-exec needs a
// binary built on gonf's CLI (errNoCLIHost). Mode none as root needs neither:
// its elevated chunks apply in-process. A plan without elevated chunks is
// never refused here.
func preflightElevation(chunks []plan.Chunk, mode privilege.Mode) error {
	ids := elevatedIDs(chunks)
	if len(ids) == 0 {
		return nil
	}
	if mode == privilege.None {
		if processEUID() == 0 {
			return nil
		}
		return fmt.Errorf("privileged ops %s need elevation, but the privilege mode is none and "+
			"this process is not root: set -privilege sudo|doas (api.SetPrivilege) or run as root",
			strings.Join(ids, ", "))
	}
	if !clihost.Active() {
		return fmt.Errorf("privileged ops %s: %w", strings.Join(ids, ", "), errNoCLIHost)
	}
	return nil
}

// elevatedIDs lists the op IDs of the elevated chunks, in chunk order, for a
// refusal message.
func elevatedIDs(chunks []plan.Chunk) []string {
	var ids []string
	for _, ch := range chunks {
		if ch.Elevate {
			ids = append(ids, chunkResourceIDs(ch)...)
		}
	}
	return ids
}

// chunkResourceIDs lists the resource op IDs of ch in order. Control ops (the
// chunk header, when_* markers) carry no ID worth naming and are skipped.
func chunkResourceIDs(ch plan.Chunk) []string {
	var ids []string
	for _, op := range ch.Ops {
		if op.ID != "" && !plan.IsControlKind(op.Op) {
			ids = append(ids, op.ID)
		}
	}
	return ids
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
// mutates nothing. preflightElevation then refuses, also before any chunk,
// elevated chunks that could not run: mode none in a non-root process, or a
// process not running gonf's CLI (whose re-exec would re-run its own main as
// root). This covers Run, which applies through ApplyChunksContext.
//
// ApplyChunks itself runs without a caller-supplied context (equivalent to
// ApplyChunksContext(context.Background(), ...)): this is the signature
// nested api.Run calls (task bodies invoking Run("othertask")) and any
// existing caller already depend on, so it is kept exactly as-is for API
// stability. New callers that can supply a cancelable/timeout-bound context
// (the CLI entry point, in particular) should use ApplyChunksContext
// instead — its elevated chunks still get DefaultChunkTimeout even when the
// given ctx has no deadline, and its in-process chunks run through
// ApplyPlanContext, whose ctx kills the backend command in flight (via
// internal/exec). File and ConfigSet validators, which run outside
// internal/exec, are the one part ctx does not reach; they read the same
// process-wide default timeout, so they stay bounded (see
// internal/validator).
func ApplyChunks(ops []plan.Op, planDir string, mode privilege.Mode) error {
	return ApplyChunksContext(context.Background(), ops, planDir, mode)
}

// ApplyChunksContext is ApplyChunks bounded/cancelable by ctx: canceling ctx
// (e.g. the CLI's SIGINT/SIGTERM context) kills an in-flight elevated
// sudo/doas re-exec or the backend command of an in-process chunk. See ApplyChunks's doc comment for why ApplyChunks itself
// keeps the old context.Background() behavior instead of taking ctx directly.
func ApplyChunksContext(ctx context.Context, ops []plan.Op, planDir string, mode privilege.Mode) error {
	chunks := plan.SplitPrivilegeChunks(ops)
	if err := plan.ValidateChunks(chunks); err != nil {
		return err
	}
	if err := preflightElevation(chunks, mode); err != nil {
		return err
	}
	return applySplitChunks(ctx, chunks, planDir, mode, chunkIndexLabel)
}

// chunkIndexLabel is ApplyChunks' wording: "chunk 1 (elevated)". Its input is
// a recorded plan, whose chunk order the caller knows.
func chunkIndexLabel(i int, ch plan.Chunk) string {
	if ch.Elevate {
		return fmt.Sprintf("chunk %d (elevated)", i)
	}
	return fmt.Sprintf("chunk %d", i)
}

// applySplitChunks applies already validated and pre-flighted chunks in
// order: user chunks in-process, privileged chunks via the sudo/doas re-exec,
// or in-process when mode is none and this process is root. The first
// failure stops the apply and is prefixed with label's name for the chunk.
func applySplitChunks(ctx context.Context, chunks []plan.Chunk, planDir string, mode privilege.Mode, label chunkLabel) error {
	for i, ch := range chunks {
		// LOCAL apply re-exec: this process's euid is the correct authority
		// here. Remote pushes must NEVER make this decision from the
		// controller's euid — see privilege.WrapApplyCmd's doc comment.
		var err error
		if !ch.Elevate || (mode == privilege.None && processEUID() == 0) {
			err = ApplyPlanContext(ctx, ch.Ops, planDir)
		} else {
			err = elevatedApplyRunner(ctx, mode, ch.Ops, planDir)
		}
		if err != nil {
			return fmt.Errorf("%s: %w", label(i, ch), err)
		}
	}
	return nil
}
