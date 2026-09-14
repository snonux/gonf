package exec

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Opts configures an optional working directory, environment, and timeout for
// RunWith. A nil Env means the process inherits the current environment. A
// non-nil Env (including an empty slice) replaces it entirely.
//
// Timeout is opt-in: 0 (the default) keeps the historical no-timeout behavior
// for existing callers. With Timeout > 0 the process is killed when the
// deadline expires and the timeout is surfaced as an error (partial
// stdout/stderr is still returned). Caveat: Wait also waits for the internal
// stdout/stderr pipes to close, so a killed command that leaks pipe-holding
// grandchildren (e.g. `sh -c 'cmd &'`) can still block past the deadline.
type Opts struct {
	Dir     string
	Env     []string
	Timeout time.Duration
}

// Run executes a command with the given arguments and returns stdout, stderr,
// exit code, and any error encountered starting the process.
func Run(name string, args ...string) (stdout, stderr string, exitCode int, err error) {
	return RunWith(Opts{}, name, args...)
}

// RunWith is like Run but applies Dir, Env, and Timeout from opts.
func RunWith(opts Opts, name string, args ...string) (stdout, stderr string, exitCode int, err error) {
	var cancel context.CancelFunc
	ctx := context.Background()
	if opts.Timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, opts.Timeout)
		defer cancel()
	}
	// With a plain Background context CommandContext behaves like Command, so
	// the no-timeout path is unchanged.
	cmd := exec.CommandContext(ctx, name, args...)
	if opts.Dir != "" {
		cmd.Dir = opts.Dir
	}
	if opts.Env != nil {
		cmd.Env = opts.Env
	}
	return runCollecting(ctx, opts.Timeout, cmd)
}

// RunWithStdin is like Run but feeds stdin (from a strings.Reader) to the
// process. It has no Dir, Env, or Timeout support; if a caller ever needs
// stdin combined with those, add an Opts.Stdin field then. The
// error/exit-code handling is identical to RunWith's: a non-zero exit is
// surfaced via exitCode rather than err.
func RunWithStdin(stdin string, name string, args ...string) (stdout, stderr string, exitCode int, err error) {
	cmd := exec.Command(name, args...)
	cmd.Stdin = strings.NewReader(stdin)
	return runCollecting(context.Background(), 0, cmd)
}

// runCollecting runs cmd, collects stdout and stderr, and maps errors to the
// shared contract: a deadline kill (ctx.Err() != nil after cmd.Run) surfaces
// as an error with exit code -1, a completed non-zero exit is reported via
// exitCode with a nil error, and any other failure returns exit code -1 with
// the error.
func runCollecting(ctx context.Context, timeout time.Duration, cmd *exec.Cmd) (stdout, stderr string, exitCode int, err error) {
	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	err = cmd.Run()

	stdout = stdoutBuf.String()
	stderr = stderrBuf.String()

	if err == nil {
		return stdout, stderr, 0, nil
	}

	// A deadline kill surfaces as *exec.ExitError ("signal: killed"), which
	// would otherwise be mistaken for a completed non-zero run: the command
	// never finished, so report the timeout as an error instead. With
	// Timeout == 0 the context is Background and ctx.Err() is always nil.
	if ctxErr := ctx.Err(); ctxErr != nil {
		return stdout, stderr, -1, fmt.Errorf("timed out after %v: %w", timeout, ctxErr)
	}
	if exitError, ok := err.(*exec.ExitError); ok {
		// The command ran and exited non-zero; surface that via exitCode
		// rather than err so callers can treat start failures separately.
		return stdout, stderr, exitError.ExitCode(), nil
	}
	return stdout, stderr, -1, err
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
