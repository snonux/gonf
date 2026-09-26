package api

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/snonux/gonf/internal/applyproto"
	"github.com/snonux/gonf/internal/clihost"
	gexec "github.com/snonux/gonf/internal/exec"
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
// that outlives it is stopped (SIGTERM, SIGKILL elevatedCancelGrace() later)
// and reported as an apply failure instead of hanging the whole process
// forever (the resilience gap this constant closes: previously the
// re-exec'd child had no timeout and no context at all).
const DefaultChunkTimeout = 10 * time.Minute

// elevatedStopNotice is printed when an interrupt arrives while an elevated
// re-exec runs; %v is elevatedCancelGrace(). The child is told to stop via
// the cancel pipe (runElevatedCmd), not by counting on the wrapper to relay
// or withhold a signal correctly, so telling the CHILD to stop is uniform
// whether the wrapper is sudo or doas. The %v grace period bound is NOT
// uniform, though (task 9d2): it is enforced only where gonf can actually
// signal the wrapper afterwards (sudo, OpenDoas) — an unprivileged SIGKILL
// of a still-root-owned native OpenBSD doas process fails with EPERM (see
// elevatedCancelGrace's doc comment), so for that wrapper gonf simply waits
// for it to exit on its own once its child has stopped, with no enforced
// bound at all if something in that chain wedges. The notice text below is
// worded to not overclaim a bound gonf cannot actually enforce in every
// case.
const elevatedStopNotice = "gonf: interrupt received; asking the elevated apply to stop, bounded by %v " +
	"only where gonf can still signal the sudo/doas wrapper afterwards (unbounded for a wrapper gonf " +
	"cannot signal at all, e.g. native OpenBSD doas) (interrupt again to force exit)\n"

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
// child ("gonf [-verbose|-quiet] [-profile=<override>] [-cmd-timeout=<d>]
// apply -cancel-pipe [-n] <path>"). Split out from defaultElevatedApply so the dry-run,
// profile-override and command-timeout propagation can be
// asserted by a unit test without spawning sudo/doas: dryRun must mirror
// resource.DryRun() at the call site (see remoteApplyCmd in
// internal/remote/remote.go for the equivalent remote-push argument), or the
// elevated child applies for real during a local "gonf -n" / "-dry-run" run.
//
// "-cancel-pipe" (internal/applyproto.CancelPipeFlag; an apply-subcommand
// flag, so it follows "apply" like "-n", and built here via
// applyproto.ElevatedArgs, the single source of truth this argv shares with
// internal/cli's cliApply flag set — see that package's doc comment for why)
// tells the child to treat its stdin as an out-of-band cancel channel: a
// successful read of the one-byte cancel signal the parent writes (see
// wireElevatedCancelPipe/applyproto.CancelByte) cancels the child's own
// context the same way a delivered SIGTERM would (see internal/cli's
// cliApply, its watchCancelPipe, and api's runElevatedCmd, which wires the
// parent end); a bare EOF with no byte ever read — the parent's write end
// closing without sending it, e.g. because the controller process itself
// died — is NOT treated as a cancel (task 6d2), so the child keeps applying
// instead of wrongly aborting an in-flight privileged command. It is always
// set here because this argv is only ever used for the local elevated
// re-exec, whose stdin runElevatedCmd dedicates to that pipe (the plan is
// passed by path, never by stdin, so nothing else needs the child's stdin).
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
//
// cmdTimeout must mirror CommandTimeout() at the call site, or the child's
// backend commands and File/ConfigSet validators run under the built-in 5m
// default instead of the parent's "-cmd-timeout" (a validated /etc file is
// applied in the root chunk, i.e. by this child). Like "-profile" it is a
// global flag and precedes "apply"; gexec.CmdTimeoutFlag leaves it out when
// cmdTimeout is the built-in default. The child is this very binary, so
// unlike the remote apply (internal/remote cmdtimeout.go) no FLAG-SUPPORT
// capability check is needed before passing it: this binary always parses
// its own "-cmd-timeout". That is not the same as sudoers/doas ARGUMENT
// matching, though (pre-existing, not new here, and shared with
// "-profile"): a rule restricted to a fixed command line (e.g. "gonf apply
// *") refuses the whole elevated re-exec the moment either flag lands in
// argv before "apply", with a bare sudo/doas refusal that never mentions
// -cmd-timeout or -profile — see docs/design/plan.md's note on fixed-argument
// sudoers rules. elevatedCancelGrace() relies on the forwarding that does
// go through: the child's validators are bound by the same timeout the
// grace is derived from.
//
// logLevel must mirror logger.GetLevel() at the call site, or the child logs
// at its default level: its lines are relayed as they are, so "gonf -quiet"
// would still print every privileged change and "gonf -verbose" none of the
// privileged debug lines. logger.LevelFlag turns it into the global
// "-verbose"/"-quiet" flag, or nothing at the default level.
func elevatedApplyArgv(exe, path string, dryRun bool, profileOverride string, cmdTimeout time.Duration, logLevel logger.Level) []string {
	argv := []string{exe}
	if flag := logger.LevelFlag(logLevel); flag != "" {
		argv = append(argv, flag)
	}
	if profileOverride != "" {
		argv = append(argv, "-profile="+profileOverride)
	}
	if flag := gexec.CmdTimeoutFlag(cmdTimeout); flag != "" {
		argv = append(argv, flag)
	}
	argv = append(argv, "apply")
	argv = append(argv, applyproto.ElevatedArgs(dryRun)...)
	return append(argv, path)
}

// defaultElevatedApply runs the elevated re-exec under ctx: canceling ctx
// (e.g. the CLI's SIGINT/SIGTERM context) stops the in-flight sudo/doas
// child (runElevatedCmd). When ctx carries no deadline of its own,
// DefaultChunkTimeout is applied so a wedged privileged command (a hung
// package manager, a systemctl call waiting on a broken unit) cannot block
// the whole apply forever — previously this used plain exec.Command with no
// context at all.
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
	argv := elevatedApplyArgv(exe, path, resource.DryRun(), ProfileOverride(), CommandTimeout(), logger.GetLevel())
	argv, err = privilege.WrapArgv(mode, true, argv)
	if err != nil {
		return err
	}
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, DefaultChunkTimeout)
		defer cancel()
	}
	return runElevatedCmd(ctx, mode, argv)
}

// elevatedCancelGrace is how long a canceled elevated re-exec gets between
// the cancel firing and the wrapper (sudo/doas) being SIGKILLed: the command
// timeout (a validator running in the child is bound by nothing shorter, and
// the child waits for it; the child runs under this same timeout because
// elevatedApplyArgv forwards "-cmd-timeout") plus twice the child's own
// command grace, so the child can stop its backend command or finish its
// validator, discard its verdict and exit before the wrapper is killed.
// Killing the wrapper earlier would orphan the root child (a wrapper without
// use_pty) or hang it up mid-op (use_pty). A second interrupt force-exits
// gonf instead of waiting (see internal/cli signalContext).
//
// This grace is delivered to the child via the cancel pipe (runElevatedCmd),
// not by signalling the wrapper: task z62 built this on the assumption that
// an unprivileged process cannot signal a doas child at all (EPERM), so
// signalling sudo (which does relay) was safe and doas silently couldn't be
// signalled either way. That assumption held for native OpenBSD doas
// (confirmed: an unprivileged SIGTERM/SIGKILL to it fails with EPERM, and
// gonf then simply waits for it to finish on its own, so no bound is
// actually enforced there) but not for OpenDoas, the doas implementation
// shipped by Linux distros such as Fedora: verified empirically against
// opendoas 6.8.2 there, an unprivileged SIGTERM to the doas process
// succeeds, and OpenDoas's own PAM parent then prints "Session terminated,
// killing shell", forwards the signal to the elevated child, and SIGKILLs it
// only about two seconds later regardless of whether that child is mid
// graceful-shutdown — cutting this grace period down to ~2s instead of
// honoring it. So all three behave differently, and none of them can be
// trusted alone: sudo forwards a signal faithfully, OpenDoas forwards it but
// then kills early anyway, and native OpenBSD doas refuses it outright. The
// pipe sidesteps this by telling the child directly, bypassing the wrapper's
// signal handling (or lack of it) entirely.
func elevatedCancelGrace() time.Duration {
	return gexec.DefaultTimeout() + 2*gexec.CancelGrace
}

// runElevatedCmd runs the wrapped re-exec argv under ctx and relays its
// output. On cancel, gonf writes the one-byte cancel signal to a pipe wired
// to the child's stdin and then closes the write end (cancelR/cancelW
// below, cancelPipeByte); elevatedApplyArgv's "-cancel-pipe" tells the child
// (internal/cli's cliApply, via its watchCancelPipe goroutine) to watch that
// fd and cancel its own context once it actually reads that byte, exactly
// as a delivered SIGTERM would — both feed the same ctx that the child's
// applyPlanOps/api.ApplyPlanContext already stops on, so the pipe is only an
// alternate trigger for that one graceful-shutdown path, not a second one.
// A bare close with no byte ever sent (e.g. this controller process itself
// dying before choosing to cancel) is deliberately NOT treated as a cancel
// by the child (task 6d2) — see watchCancelPipe's doc comment. This works
// uniformly for sudo, OpenDoas and native OpenBSD doas, because it never
// depends on the wrapper relaying (or refusing) a signal — see
// elevatedCancelGrace's doc comment for why that
// signal-only approach was unreliable. gonf also still sends the wrapper an
// explicit SIGTERM, but only when it is sudo: sudo relays signals correctly,
// so this is a harmless, cheap defense-in-depth for setups the pipe alone
// might not cover — a wrapper too old to preserve fd 0 verbatim, or
// (plausible but, per sudo's own "use_pty has no effect [unless] sudo is
// running in a terminal" doc, NOT reproduced against gonf's own
// non-interactive, piped invocation; still unverified either way, see
// docs/design/plan.md's "Fixed-argument sudoers/doas rules" section) a sudoers
// "use_pty" wrapper whose pty relay might not propagate a single stdin byte
// the same way a pipe does. It is deliberately NOT sent for doas: an
// unprivileged SIGTERM to a doas process reaches OpenDoas's own
// premature-kill logic (elevatedCancelGrace) even though the pipe already
// asked the child to stop gracefully, so signalling doas would reintroduce
// exactly the bug this pipe exists to fix; native OpenBSD doas would just
// reject it anyway. Either way, SIGKILL of the wrapper itself still follows
// only elevatedCancelGrace() later (cmd.WaitDelay, set by
// gexec.SetGracefulCancel below) IF gonf can still signal it by then — an
// unprivileged SIGKILL of a still-root-owned native OpenBSD doas process
// fails with EPERM (see elevatedCancelGrace's doc comment), so for that one
// wrapper there is in fact no enforced bound here at all, only wherever
// gonf may actually signal the wrapper (sudo and OpenDoas). Once the child
// (stopped via the pipe) has had its full grace period to exit on its own,
// the wrapper should have exited right behind it regardless.
func runElevatedCmd(ctx context.Context, mode privilege.Mode, argv []string) error {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	grace := elevatedCancelGrace()
	gexec.SetGracefulCancel(cmd, grace)

	cleanup, err := wireElevatedCancelPipe(cmd, mode)
	if err != nil {
		return err
	}
	defer cleanup()

	// afterFuncJoined (api/apply_interrupt.go), not a bare context.AfterFunc:
	// its stop blocks until this notice goroutine has actually finished
	// touching os.Stderr (a package-level var) if it started at all, so it
	// cannot still be running after runElevatedCmd itself has returned. A
	// bare context.AfterFunc's stop does not wait for an already-started f,
	// which used to let this goroutine outlive the test that triggered it
	// and race a later test's os.Stderr swap (task 1d2).
	defer afterFuncJoined(ctx, func() {
		if errors.Is(ctx.Err(), context.Canceled) {
			_, _ = fmt.Fprintf(os.Stderr, elevatedStopNotice, grace)
		}
	})()
	// The elevated child has no secret registry of its own: its log lines
	// and summary reach the operator's terminal through the controller's
	// redactor, line by line. A descendant still holding the relay pipe after
	// the child exited or was killed (an orphaned root gonf apply once the
	// context killed the wrapper) delays the return by at most
	// logger.RelayWaitDelay; then its output is handed, unredacted, to a
	// detached cat writing to stderr, so it keeps a reader even after gonf
	// exits and is never killed by SIGPIPE mid-apply (logger.RunRelayed).
	// If instead this controller process itself dies while the child is
	// still running (SIGKILLed, OOM-killed, crashed), none of the above
	// runs at all; the child survives that separately, by ignoring SIGPIPE
	// for its own run (internal/cli's cliApply, ignoreSIGPIPEForRelayedChild,
	// task lb2) — gated on "-cancel-pipe" actually being set (task 7d2), which
	// elevatedApplyArgv always sets for exactly this child, so the survival
	// guarantee described here holds unconditionally for it.
	err = logger.RunRelayed(cmd, os.Stderr)
	if err != nil && ctx.Err() != nil {
		return fmt.Errorf("%w (elevated apply stopped by context: %v)", ctx.Err(), err)
	}
	return err
}

// wireElevatedCancelPipe gives cmd (already passed through
// gexec.SetGracefulCancel, so cmd.Cancel is its SIGTERM-the-wrapper closure
// and cmd.WaitDelay is already set) an out-of-band cancel channel: cmd.Stdin
// becomes the read end of a fresh pipe (elevatedApplyArgv never uses the
// child's stdin for anything else, see its doc comment), and cmd.Cancel is
// replaced with one that writes the one-byte cancel signal and then closes
// the write end — this is the primary, wrapper-independent trigger the
// child's watchCancelPipe reacts to — and only then, for mode Sudo, still
// calls the original SIGTERM closure too (kept as defense-in-depth; never
// for doas, see runElevatedCmd's doc comment for why that would reintroduce
// the bug the pipe exists to fix).
//
// The write end is also closed by the returned cleanup, which the caller
// defers unconditionally (whether or not cmd.Cancel ever ran, i.e. whether
// or not this apply was actually canceled): that path deliberately never
// writes the cancel byte, only closes — by the time cleanup runs, cmd has
// already finished one way or another (runElevatedCmd defers it after
// logger.RunRelayed returns), so a bare, byte-less close there can never be
// mistaken by a still-running child for a real cancel; it exists only to
// release the pipe's fds, not to signal anything. Both cmd.Cancel and
// cleanup close the write end through the same sync.Once, so whichever runs
// first is the one whose intent (cancel-with-byte, or plain close) sticks;
// closing is idempotent and safe even if the child never started or already
// exited.
func wireElevatedCancelPipe(cmd *exec.Cmd, mode privilege.Mode) (cleanup func(), err error) {
	cancelR, cancelW, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("elevated apply: cancel pipe: %w", err)
	}
	cmd.Stdin = cancelR

	sigTerm := cmd.Cancel
	var closeCancelOnce sync.Once
	closeCancelPipe := func() { closeCancelOnce.Do(func() { _ = cancelW.Close() }) }
	cmd.Cancel = func() error {
		closeCancelOnce.Do(func() {
			// Best-effort: a write failure here (e.g. the child already
			// exited and nothing reads the pipe any more) is harmless — the
			// Close right after still delivers EOF, and closing without a
			// byte having landed is exactly the "not a cancel" case anyway,
			// which is moot once nothing is left to read it.
			_, _ = cancelW.Write([]byte{applyproto.CancelByte})
			_ = cancelW.Close()
		})
		if mode == privilege.Sudo {
			return sigTerm()
		}
		return nil
	}

	// cancelR (the parent's own copy of the read end) is never read from —
	// only the child's dup, inherited across the wrapper, is — so there is no
	// Start-only hook available to close it earlier; holding it open for the
	// whole apply is harmless, since EOF delivery depends only on cancelW's
	// refcount, not on how many read-end copies remain open.
	return func() { _ = cancelR.Close(); closeCancelPipe() }, nil
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
// internal/exec, are the one part ctx does not stop; they read the same
// process-wide default timeout, so they stay bounded, and a verdict that
// arrives after ctx is done is discarded (see internal/validator).
func ApplyChunks(ops []plan.Op, planDir string, mode privilege.Mode) error {
	return ApplyChunksContext(context.Background(), ops, planDir, mode)
}

// ApplyChunksContext is ApplyChunks bounded/cancelable by ctx: canceling ctx
// (e.g. the CLI's SIGINT/SIGTERM context) stops an in-flight elevated
// sudo/doas re-exec or the backend command of an in-process chunk. An
// in-process chunk gets SIGTERM first and SIGKILL only after a grace period
// (internal/exec SetGracefulCancel); an elevated re-exec is stopped via its
// cancel pipe instead (see runElevatedCmd's doc comment for why a signal to
// the wrapper alone is not used), with the wrapper itself SIGKILLed only
// after elevatedCancelGrace() if it is still alive by then AND gonf can
// actually signal it (not native OpenBSD doas, see elevatedStopNotice's
// doc comment). See ApplyChunks's
// doc comment for why ApplyChunks itself keeps the old context.Background()
// behavior instead of taking ctx directly.
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
