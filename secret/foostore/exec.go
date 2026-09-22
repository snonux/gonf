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
// caller refuses an overflowing result instead.
type boundedBuffer struct {
	buf      []byte
	max      int
	total    int
	overflow bool
}

// Write implements io.Writer.
func (b *boundedBuffer) Write(p []byte) (int, error) {
	b.total += len(p)
	if b.overflow || len(b.buf)+len(p) > b.max {
		b.overflow = true
		clear(b.buf)
		b.buf = nil
		return len(p), nil
	}
	b.buf = append(b.buf, p...)
	return len(p), nil
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
	}
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

// passFeed writes the passphrase into the pipe the child inherits.
type passFeed struct {
	r, w *os.File
	pass []byte
	done chan struct{}
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

// finish waits for the writer after the child exited. A child that never
// read the pipe has closed it by exiting, so the write fails (EPIPE, no
// signal: it is not stdout/stderr) and the goroutine ends.
func (f *passFeed) finish() {
	if f.w == nil {
		return
	}
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
