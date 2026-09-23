package foostore

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// passFDEnv and passFD tell foostore where to read the passphrase: the
// first ExtraFiles entry is descriptor 3 in the child.
const (
	passFDEnv = "FOOSTORE_READ_PASSPHRASE_FD"
	passFD    = "3"
)

// stderrCap bounds how much stderr is buffered; it is only counted, never
// shown, so a small cap is enough to report "N bytes".
const stderrCap = 64 << 10

// waitDelay bounds how long Wait waits for the output pipes after the
// process exited or was killed (a descendant still holding them).
const waitDelay = time.Second

// result is the outcome of one foostore run. stdout may hold secret bytes;
// only its length and the other fields may reach an error message.
type result struct {
	stdout    []byte
	overflow  bool  // stdout exceeded its bound and was discarded
	stderrLen int   // bytes written to stderr (content withheld)
	exitCode  int   // -1 when the process did not exit normally
	startErr  error // the process could not be started
	waitErr   error // the raw Wait error (exit status, signal, pipe)
	timedOut  bool  // the adapter's own deadline killed the process
}

// boundedBuffer keeps at most max bytes and counts the rest. It never fails
// a write, so the child is not blocked or killed by SIGPIPE mid-write; the
// caller refuses an overflowing result instead. It grows its array itself
// and overwrites each array it outgrows, so no stale copy of the bytes is
// left behind by growth (the copy buffer os/exec reads the pipe through is
// beyond its reach; see docs/secrets.md: overwriting is best effort).
type boundedBuffer struct {
	buf      []byte
	max      int
	total    int
	overflow bool
}

// passFeed writes the passphrase into the pipe the child inherits.
type passFeed struct {
	r, w *os.File
	pass []byte
	done chan struct{}
}

// Write implements io.Writer.
func (b *boundedBuffer) Write(p []byte) (int, error) {
	b.total += len(p)
	if b.overflow || len(b.buf)+len(p) > b.max {
		b.overflow = true
		clear(b.buf[:cap(b.buf)])
		b.buf = nil
		return len(p), nil
	}
	b.grow(len(p))
	b.buf = append(b.buf, p...)
	return len(p), nil
}

// grow makes room for n more bytes (at most up to max), moving the bytes to
// a new array and clearing the old one.
func (b *boundedBuffer) grow(n int) {
	need := len(b.buf) + n
	if need <= cap(b.buf) {
		return
	}
	newCap := min(max(2*cap(b.buf), need, 512), b.max)
	grown := make([]byte, len(b.buf), newCap)
	copy(grown, b.buf)
	clear(b.buf[:cap(b.buf)])
	b.buf = grown
}

// run executes the configured binary with args and collects its outcome.
// The child gets stdin from /dev/null, a new session without a controlling
// terminal (so no prompt can reach an operator's tty), the minimal
// environment of childEnv, and, when pass is set, the passphrase through an
// inherited pipe. ctx ending kills the whole process group. At most
// maxStdout bytes of stdout are kept.
func (p *Provider) run(ctx context.Context, args []string, pass []byte, maxStdout int) result {
	cmd := p.command(ctx, args, pass != nil)
	stdout := &boundedBuffer{max: maxStdout}
	stderr := &boundedBuffer{max: stderrCap}
	cmd.Stdout, cmd.Stderr = stdout, stderr

	feed, err := attachPassphrase(cmd, pass)
	if err != nil {
		return result{startErr: err, exitCode: -1}
	}
	if err := cmd.Start(); err != nil {
		feed.abort()
		return result{startErr: err, exitCode: -1}
	}
	feed.start()
	waitErr := cmd.Wait()
	feed.finish()
	code := -1
	if cmd.ProcessState != nil {
		code = cmd.ProcessState.ExitCode() // -1 when killed by a signal
	}
	return result{
		stdout:    stdout.buf,
		overflow:  stdout.overflow,
		stderrLen: stderr.total,
		exitCode:  code,
		waitErr:   waitErr,
		timedOut:  killedByDeadline(ctx, code),
	}
}

// killedByDeadline reports that ctx ending killed the process: ctx is done
// AND the process did not exit on its own (exit code -1: a signal). A
// process that exited normally while the deadline passed keeps its own
// result, so a successful read racing the deadline is not misreported as a
// timeout (and a timeout exit of foostore's own stays exit 1).
func killedByDeadline(ctx context.Context, exitCode int) bool {
	return ctx.Err() != nil && exitCode < 0
}

// command builds the exec.Cmd: argv via the (test) prefix, a new session,
// and a Cancel that kills the process group rather than only the leader.
func (p *Provider) command(ctx context.Context, args []string, withPassFD bool) *exec.Cmd {
	argv := append(append([]string(nil), p.prefix...), args...)
	cmd := exec.CommandContext(ctx, p.cfg.Binary, argv...)
	cmd.Env = p.childEnv(withPassFD)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Cancel = func() error {
		// The child leads its own session and process group (Setsid), so
		// -pid reaches anything it started as well.
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
			return cmd.Process.Kill()
		}
		return nil
	}
	cmd.WaitDelay = waitDelay
	return cmd
}

// scrub overwrites the captured stdout of a result that is not returned.
func (r *result) scrub() {
	clear(r.stdout)
	r.stdout = nil
}

// attachPassphrase adds the passphrase pipe to cmd (descriptor 3 in the
// child). With no passphrase it returns an inert feed.
func attachPassphrase(cmd *exec.Cmd, pass []byte) (*passFeed, error) {
	if pass == nil {
		return &passFeed{}, nil
	}
	r, w, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	cmd.ExtraFiles = []*os.File{r}
	return &passFeed{r: r, w: w, pass: pass, done: make(chan struct{})}, nil
}

// start closes the parent's read end (the child has its copy) and writes
// the passphrase in the background; closing the write end is the EOF
// foostore reads to.
func (f *passFeed) start() {
	if f.w == nil {
		return
	}
	_ = f.r.Close()
	go func() {
		defer close(f.done)
		_, _ = f.w.Write(f.pass)
		_ = f.w.Close()
	}()
}

// finish ends the writer after the child exited. It closes the write end
// first: a descendant of the child may still hold the read end without
// reading it, and a passphrase larger than the pipe buffer would otherwise
// block the write — and finish — forever. Closing the (non-blocking,
// poller-managed) pipe makes a pending write return at once. A child that
// read the passphrase has let the writer finish already; the second Close
// is harmless.
func (f *passFeed) finish() {
	if f.w == nil {
		return
	}
	_ = f.w.Close()
	<-f.done
}

// abort releases both ends when the child could not be started.
func (f *passFeed) abort() {
	if f.w == nil {
		return
	}
	_ = f.r.Close()
	_ = f.w.Close()
}
