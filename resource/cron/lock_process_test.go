package cron

// Cross-process lock test: a helper process (this test binary re-run with
// -test.run=^TestCrontabLockHelper$) holds the crontab lock while the parent
// test tries to take it.
//
// The helper used to be the source of cascading failures: it derived its
// lock file name independently (its own host lookup, the package-wide lock
// directory), and a failed parent assertion neither released the parent's
// accidental lock nor closed the helper's stdin, so the helper kept its lock
// until the whole test binary exited. Every later test and -count iteration
// using the same lock file then timed out after crontabLockTimeout.
//
// Now the parent hands the helper exactly the lock directory (private to
// the test) and host name it uses itself, waits for the handshake with a
// deadline sized for a heavily loaded machine, and a t.Cleanup always
// closes the helper's stdin, kills it if it does not exit in time, and
// reaps it.

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

// Environment handed from TestCrontabLockSerializesSeparateProcesses to its
// helper process: lockHelperEnv selects helper mode, and the others carry
// the crontab account, lock directory and host name the parent uses, so
// both processes contend on the same lock file.
const (
	lockHelperEnv = "GONF_CRON_LOCK_HELPER"
	lockUserEnv   = "GONF_CRON_LOCK_USER"
	lockDirEnv    = "GONF_CRON_TEST_LOCK_DIR"
	lockHostEnv   = "GONF_CRON_TEST_LOCK_HOST"
)

// Helper process deadlines. Starting a -race test binary can take seconds
// on a loaded machine, so the handshake deadline is generous; it only
// bounds a helper that is genuinely stuck. The exit deadline bounds the
// wait after stdin is closed before the helper is killed.
const (
	lockHelperStartTimeout = 2 * time.Minute
	lockHelperExitTimeout  = 30 * time.Second
	lockHelperReady        = "locked"
)

// configureLockHelperProcess runs in the helper process (from TestMain): it
// adopts the parent's lock directory and host name instead of deriving its
// own, so the helper cannot end up on a different lock file than the parent.
func configureLockHelperProcess() {
	crontabLockDirOverride = os.Getenv(lockDirEnv)
	host := os.Getenv(lockHostEnv)
	lockHostName = func() (string, error) { return host, nil }
}

func TestCrontabLockSerializesSeparateProcesses(t *testing.T) {
	userName := currentCronUser(t)
	helper := startLockHelper(t, useTestLockDir(t), userName)
	helper.waitLocked(t)

	// The contended attempt must fail with the timeout, and return well
	// before the default crontabLockTimeout (the bound is loose because a
	// loaded machine delays the retry loop).
	start := time.Now()
	unlock, err := lockCrontabWithin(userName, 40*time.Millisecond)
	if err == nil {
		_ = unlock() // never leak a lock into later tests
		t.Fatal("second process acquired lock before release")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("contended lock: err=%v, want a timeout", err)
	}
	if elapsed := time.Since(start); elapsed > crontabLockTimeout/2 {
		t.Fatalf("contended lock did not time out promptly: %s", elapsed)
	}
	if err := helper.release(); err != nil {
		t.Fatal(err)
	}
	unlock, err = lockCrontabWithin(userName, crontabLockTimeout)
	if err != nil {
		t.Fatalf("second process lock after release: %v", err)
	}
	if err := unlock(); err != nil {
		t.Fatal(err)
	}
}

// TestCrontabLockHelper is the other process of
// TestCrontabLockSerializesSeparateProcesses; it is a no-op otherwise. It
// takes the lock, announces lockHelperReady on stdout and holds the lock
// until its stdin reaches EOF (the parent closes it, or exits).
func TestCrontabLockHelper(t *testing.T) {
	if os.Getenv(lockHelperEnv) != "1" {
		return
	}
	unlock, err := lockCrontab(os.Getenv(lockUserEnv))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := unlock(); err != nil {
			t.Error(err)
		}
	}()
	if _, err := fmt.Fprintln(os.Stdout, lockHelperReady); err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(io.Discard, os.Stdin); err != nil {
		t.Fatal(err)
	}
}

// lockHelper is a running helper process. locked is closed when the helper
// reports that it holds the lock; exited is closed once the process has
// been reaped, after which exitErr is set. output keeps everything the
// helper printed, for failure messages.
type lockHelper struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	locked  chan struct{}
	exited  chan struct{}
	exitErr error

	outputMu sync.Mutex
	output   strings.Builder

	finishOnce sync.Once
	finishErr  error
}

// startLockHelper starts the helper process on dir with this process's
// host name and registers a cleanup that always stops and reaps it.
func startLockHelper(t *testing.T, dir, userName string) *lockHelper {
	t.Helper()
	host, err := lockHostName()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestCrontabLockHelper$", "-test.count=1")
	cmd.Env = append(os.Environ(), lockHelperEnv+"=1", lockUserEnv+"="+userName, lockDirEnv+"="+dir, lockHostEnv+"="+host)
	helper := &lockHelper{cmd: cmd, locked: make(chan struct{}), exited: make(chan struct{})}
	if helper.stdin, err = cmd.StdinPipe(); err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go helper.watch(stdout)
	t.Cleanup(func() {
		if err := helper.finish(); err != nil {
			t.Logf("lock helper: %v", err)
		}
	})
	return helper
}

// watch reads the helper's stdout until EOF, signalling the handshake line,
// and then reaps the process. exec.Cmd.Wait may only run after all reads
// from the stdout pipe have finished, so this goroutine owns both.
func (h *lockHelper) watch(stdout io.Reader) {
	scanner := bufio.NewScanner(stdout)
	signalled := false
	for scanner.Scan() {
		line := scanner.Text()
		h.outputMu.Lock()
		h.output.WriteString(line + "\n")
		h.outputMu.Unlock()
		if line == lockHelperReady && !signalled {
			signalled = true
			close(h.locked)
		}
	}
	h.exitErr = h.cmd.Wait()
	close(h.exited)
}

// waitLocked blocks until the helper holds the lock, and fails the test if
// the helper exits first or does not report within lockHelperStartTimeout.
func (h *lockHelper) waitLocked(t *testing.T) {
	t.Helper()
	timer := time.NewTimer(lockHelperStartTimeout)
	defer timer.Stop()
	select {
	case <-h.locked:
	case <-h.exited:
		t.Fatalf("lock helper exited before taking the lock (%v): %s", h.exitErr, h.printed())
	case <-timer.C:
		t.Fatalf("lock helper did not take the lock within %s: %s", lockHelperStartTimeout, h.printed())
	}
}

// release makes the helper drop its lock and exit, and reports whether it
// exited cleanly.
func (h *lockHelper) release() error { return h.finish() }

// finish closes the helper's stdin (its signal to unlock and exit), waits up
// to lockHelperExitTimeout, then kills it, and always reaps it. It is
// idempotent, so the cleanup after release is a no-op.
func (h *lockHelper) finish() error {
	h.finishOnce.Do(func() {
		_ = h.stdin.Close()
		timer := time.NewTimer(lockHelperExitTimeout)
		defer timer.Stop()
		select {
		case <-h.exited:
		case <-timer.C:
			_ = h.cmd.Process.Kill()
			<-h.exited
			h.finishErr = fmt.Errorf("did not exit within %s of release and was killed: %s", lockHelperExitTimeout, h.printed())
			return
		}
		if h.exitErr != nil {
			h.finishErr = errors.Join(h.exitErr, errors.New(h.printed()))
		}
	})
	return h.finishErr
}

// printed returns what the helper has written to stdout so far.
func (h *lockHelper) printed() string {
	h.outputMu.Lock()
	defer h.outputMu.Unlock()
	return fmt.Sprintf("%q", h.output.String())
}
