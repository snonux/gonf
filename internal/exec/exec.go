package exec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Opts configures an optional working directory, environment, and timeout for
// RunWith. A nil Env means the process inherits the current environment. A
// non-nil Env (including an empty slice) replaces it entirely.
//
// Timeout selects the deadline applied to the process: 0 (the default) uses
// the process-wide default timeout (see DefaultTimeout/SetDefaultTimeout), a
// positive value overrides it for this call only, and a negative value opts
// out of any deadline (the historical no-timeout behavior, for the rare
// caller that genuinely needs it). With a deadline in effect the process is
// stopped when it expires (SIGTERM, then SIGKILL after CancelGrace; see
// SetGracefulCancel) and the timeout is surfaced as an error (partial
// stdout/stderr is still returned). A grandchild still holding the output
// pipes after the command exited or was stopped (e.g. `sh -c 'cmd &'`, a
// daemon that kept its fds) is cut off CancelGrace later instead of blocking
// the call.
type Opts struct {
	Dir     string
	Env     []string
	Timeout time.Duration
}

// CancelGrace is how long a canceled or timed-out command gets between the
// SIGTERM that asks it to stop and the SIGKILL that forces it, and how long
// Wait keeps draining output pipes a lingering grandchild still holds (both
// are exec.Cmd.WaitDelay). Package managers (dnf/rpm, apt/dpkg, pkg) that
// were interrupted mid-transaction use it to shut down cleanly instead of
// being killed outright.
const CancelGrace = 10 * time.Second

// cancelGrace is the grace RunWith and RunWithStdin use: CancelGrace,
// shortened by tests only.
var cancelGrace = CancelGrace

// Like the resource package's dry-run flag, the default timeout is
// process-wide DSL-style state: set once at startup (e.g. from the CLI's
// -cmd-timeout flag) before any Run/RunWith/RunWithStdin call, not mutated
// concurrently with an in-flight apply.
var (
	timeoutMu      sync.Mutex
	defaultTimeout = 5 * time.Minute
)

// The bound context is the parent of every command Run, RunWith and
// RunWithStdin start (context.Background() unless BindContext installed
// one). It is process-wide for the same reason as the default timeout: the
// resource backends call Run/RunWith directly, deep below plan.Apply, and
// threading a ctx parameter through every backend and plan handler would
// change all of their signatures. A plan apply binds its caller's ctx here
// for its duration (plan.ApplyWithContext), so canceling it (the CLI's
// SIGINT/SIGTERM context) stops the command in flight.
var (
	boundMu  sync.Mutex
	boundCtx = context.Background()
)

// SetDefaultTimeout overrides the default timeout applied by Run,
// RunWithStdin, and RunWith when Opts.Timeout is left at its zero value. It
// bounds every resource backend's package-manager/systemctl/crontab/rcctl
// invocation that goes through this package, so a hung local or remote
// command cannot block gonf forever. File WithValidation and ConfigSet
// WithSetValidation validators run outside this package (internal/validator,
// which caps their output) but read DefaultTimeout, so the same bound applies
// to them. d <= 0
// is rejected (callers that want no timeout at all use Opts.Timeout < 0 for
// that one call, not a process-wide unlimited default).
func SetDefaultTimeout(d time.Duration) {
	if d <= 0 {
		return
	}
	timeoutMu.Lock()
	defer timeoutMu.Unlock()
	defaultTimeout = d
}

// DefaultTimeout returns the current process-wide default timeout.
func DefaultTimeout() time.Duration {
	timeoutMu.Lock()
	defer timeoutMu.Unlock()
	return defaultTimeout
}

// BindContext makes ctx the parent context of every command Run, RunWith and
// RunWithStdin start from now on, until the returned restore func reinstates
// the previously bound context. Canceling ctx stops a command in flight
// (SIGTERM, SIGKILL after CancelGrace) and refuses to start new ones; either
// surfaces as a "canceled" error with exit code -1. The per-call timeout still applies on
// top of ctx. A nil ctx binds context.Background(). Like SetDefaultTimeout
// it is process-wide state for a sequential apply: bind/restore pairs must
// nest (defer restore()), not interleave from concurrent goroutines.
func BindContext(ctx context.Context) (restore func()) {
	if ctx == nil {
		ctx = context.Background()
	}
	boundMu.Lock()
	defer boundMu.Unlock()
	prev := boundCtx
	boundCtx = ctx
	return func() {
		boundMu.Lock()
		defer boundMu.Unlock()
		boundCtx = prev
	}
}

// SetGracefulCancel makes cmd, built with exec.CommandContext, stop
// gracefully when its context is done: it is sent SIGTERM instead of
// os/exec's default immediate SIGKILL, and only if it is still running grace
// later is it killed. The same WaitDelay also bounds how long Wait drains
// output pipes (the goroutine-copied ones, not an *os.File) that a
// grandchild holds after the command itself exited, so such a pipe holder
// can delay the return by at most grace. Only the command itself is
// signalled, not its descendants: it shares gonf's process group, so a
// group signal would hit gonf too, and a terminal Ctrl-C already reached the
// whole foreground group. A command that exits 0 while a grandchild still
// holds its pipes makes Wait return exec.ErrWaitDelay; RunWith treats that
// as the success it is.
func SetGracefulCancel(cmd *exec.Cmd, grace time.Duration) {
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = grace
}

// boundContext returns the context installed by BindContext.
func boundContext() context.Context {
	boundMu.Lock()
	defer boundMu.Unlock()
	return boundCtx
}

// Run executes a command with the given arguments and returns stdout, stderr,
// exit code, and any error encountered starting the process. It is bounded by
// the process-wide default timeout (DefaultTimeout/SetDefaultTimeout).
func Run(name string, args ...string) (stdout, stderr string, exitCode int, err error) {
	return RunWith(Opts{}, name, args...)
}

// RunWith is like Run but applies Dir, Env, and Timeout from opts.
func RunWith(opts Opts, name string, args ...string) (stdout, stderr string, exitCode int, err error) {
	timeout := effectiveTimeout(opts.Timeout)

	var cancel context.CancelFunc
	parent := boundContext()
	ctx := parent
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	// With a plain Background context (nothing bound) the (opt-in,
	// Opts.Timeout < 0) no-timeout path is never canceled; a bound context
	// can still cancel it.
	cmd := exec.CommandContext(ctx, name, args...)
	if opts.Dir != "" {
		cmd.Dir = opts.Dir
	}
	if opts.Env != nil {
		cmd.Env = opts.Env
	}
	return runCollecting(parent, ctx, timeout, cmd)
}

// effectiveTimeout resolves an Opts.Timeout value against the process-wide
// default: 0 means "use the default", negative means "no timeout at all",
// and a positive value is returned unchanged.
func effectiveTimeout(t time.Duration) time.Duration {
	switch {
	case t == 0:
		return DefaultTimeout()
	case t < 0:
		return 0
	default:
		return t
	}
}

// RunWithStdin is like Run but feeds stdin (from a strings.Reader) to the
// process. It always applies the process-wide default timeout (no per-call
// override); if a caller ever needs a different timeout combined with stdin,
// add an Opts.Stdin field to RunWith instead. The error/exit-code handling is
// identical to RunWith's: a non-zero exit is surfaced via exitCode rather
// than err.
func RunWithStdin(stdin string, name string, args ...string) (stdout, stderr string, exitCode int, err error) {
	timeout := DefaultTimeout()
	parent := boundContext()
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = strings.NewReader(stdin)
	return runCollecting(parent, ctx, timeout, cmd)
}

// runCollecting runs cmd (built on ctx, a child of the bound context parent
// carrying the per-call timeout) with graceful cancellation
// (SetGracefulCancel), collects stdout and stderr, and maps errors to the
// shared contract: a deadline or cancellation stop (ctx.Err() != nil after
// cmd.Run) surfaces as an error with exit code -1, a completed non-zero exit
// is reported via exitCode with a nil error, and any other failure returns
// exit code -1 with the error.
func runCollecting(parent, ctx context.Context, timeout time.Duration, cmd *exec.Cmd) (stdout, stderr string, exitCode int, err error) {
	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf
	SetGracefulCancel(cmd, cancelGrace)

	err = cmd.Run()

	stdout = stdoutBuf.String()
	stderr = stderrBuf.String()

	// ErrWaitDelay: the command exited 0 and only a grandchild kept the
	// output pipes open until the grace ran out; the command succeeded.
	if err == nil || errors.Is(err, exec.ErrWaitDelay) {
		return stdout, stderr, 0, nil
	}

	// A deadline or cancellation stop surfaces as *exec.ExitError ("signal:
	// terminated" or "signal: killed"), which would otherwise be mistaken for
	// a completed non-zero run: the command never finished, so report it as
	// an error instead. With no deadline in effect (timeout == 0, an explicit
	// Opts.Timeout < 0) and nothing bound, ctx is Background and never done.
	if ctxErr := ctx.Err(); ctxErr != nil {
		return stdout, stderr, -1, contextError(parent, timeout, ctxErr)
	}
	if exitError, ok := err.(*exec.ExitError); ok {
		// The command ran and exited non-zero; surface that via exitCode
		// rather than err so callers can treat start failures separately.
		return stdout, stderr, exitError.ExitCode(), nil
	}
	return stdout, stderr, -1, err
}

// contextError words a command's context failure by its cause: a
// cancellation of the bound context (BindContext, e.g. SIGINT/SIGTERM) as
// "canceled", a deadline the bound context carried itself (the caller's own,
// possibly shorter than the per-call timeout) without naming a duration, and
// the per-call timeout as the "timed out after <timeout>" it historically
// was.
func contextError(parent context.Context, timeout time.Duration, ctxErr error) error {
	switch {
	case errors.Is(ctxErr, context.Canceled):
		return fmt.Errorf("canceled: %w", ctxErr)
	case parent.Err() != nil:
		return fmt.Errorf("caller deadline exceeded: %w", ctxErr)
	default:
		return fmt.Errorf("timed out after %v: %w", timeout, ctxErr)
	}
}

// MergeEnv returns a full environment slice: the current process environment
// with each key in extra overriding or appending. Keys in extra with an empty
// value still set that key to "".
func MergeEnv(extra map[string]string) []string {
	if len(extra) == 0 {
		return os.Environ()
	}

	base := os.Environ()
	out := make([]string, 0, len(base)+len(extra))
	seen := make(map[string]struct{}, len(extra))

	for _, kv := range base {
		key, _, ok := splitEnv(kv)
		if !ok {
			out = append(out, kv)
			continue
		}
		if val, override := extra[key]; override {
			out = append(out, key+"="+val)
			seen[key] = struct{}{}
			continue
		}
		out = append(out, kv)
	}

	for key, val := range extra {
		if _, ok := seen[key]; ok {
			continue
		}
		out = append(out, key+"="+val)
	}
	return out
}

func splitEnv(kv string) (key, val string, ok bool) {
	for i := 0; i < len(kv); i++ {
		if kv[i] == '=' {
			return kv[:i], kv[i+1:], true
		}
	}
	return "", "", false
}
