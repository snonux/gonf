package validator

import (
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	"golang.org/x/sys/unix"
)

// A validator runs as the leader of its own process group (Setpgid), so a
// timeout can kill it together with every process it started (task b82): a
// wrapper script whose hung child (e.g. nsd-checkzone) would otherwise
// survive each apply and pile up on the host. Setpgid, kill(-pgid, sig) and
// the forwarded signals behave the same on Linux, FreeBSD, OpenBSD, NetBSD
// and macOS; gonf builds for these unix systems only.
//
// Its own group takes the validator out of gonf's (foreground) process
// group, so the terminal no longer signals it. signalForwarder restores that
// for the terminating signals; see its comment for what is not forwarded.

// signalForwarder relays the first terminating signal gonf receives while a
// validator runs to the validator's process group, where the terminal (or a
// supervisor signalling gonf) would have delivered it had the validator
// stayed in gonf's group. It handles one signal only: it then unregisters
// and re-raises that signal on gonf itself, so gonf's own disposition applies
// exactly as without the forwarder. A program without a handler of its own
// (e.g. a library caller of api.Apply) still dies of Ctrl-C, and gonf's CLI,
// which handles SIGINT/SIGTERM via signal.NotifyContext, has already been
// notified by the original signal and just receives it a second time.
//
// Not forwarded: job-control signals (Ctrl-Z/SIGTSTP, SIGCONT), so Ctrl-Z
// stops gonf but not the validator, which keeps running and stays bounded by
// the timeout; a second terminating signal during the same validator run;
// and signals gonf ignores (e.g. SIGHUP under nohup), which the validator
// inherits as ignored anyway. Being in a background process group, a
// validator that reads the controlling terminal (e.g. a sudo password
// prompt) is stopped by SIGTTIN until the timeout kills it.
type signalForwarder struct {
	signals  chan os.Signal
	done     chan struct{}
	finished chan struct{}
	started  bool
	// reraise delivers a forwarded signal to gonf itself; tests replace it,
	// since the default would terminate the test binary.
	reraise func(os.Signal)
}

// newSignalForwarder registers for the terminating signals gonf does not
// ignore. It must be called before the validator starts, so a signal
// arriving while it starts is buffered instead of missed; start then begins
// relaying and stop must always follow.
func newSignalForwarder() *signalForwarder {
	f := &signalForwarder{
		signals:  make(chan os.Signal, 1),
		done:     make(chan struct{}),
		finished: make(chan struct{}),
		reraise:  reraiseOnSelf,
	}
	var watched []os.Signal
	for _, sig := range []os.Signal{unix.SIGINT, unix.SIGTERM, unix.SIGHUP, unix.SIGQUIT} {
		if !signal.Ignored(sig) {
			watched = append(watched, sig)
		}
	}
	// signal.Notify without signals would relay every signal, so a
	// forwarder with nothing to watch simply never receives one.
	if len(watched) > 0 {
		signal.Notify(f.signals, watched...)
	}
	return f
}

// runInOwnGroup starts cmd as the leader of a new process group, relays
// terminating signals to that group while it runs (signalForwarder) and
// waits for it. It returns what cmd.Run would have returned.
func runInOwnGroup(cmd *exec.Cmd) error {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	forwarder := newSignalForwarder()
	defer forwarder.stop()
	if err := cmd.Start(); err != nil {
		return err
	}
	forwarder.start(cmd.Process)
	return cmd.Wait()
}

// signalGroup sends sig to the process group led by leader. A leader that
// Wait already reaped yields os.ErrProcessDone without signalling anything:
// its pid, and so the group id, may already name unrelated processes. A
// group with no member left (ESRCH) is reported as os.ErrProcessDone too.
// The check and the kill are not atomic; a reap in between leaves a window
// of microseconds in which the id could be reused, the same as for any
// pid-based kill.
func signalGroup(leader *os.Process, sig syscall.Signal) error {
	if err := leader.Signal(syscall.Signal(0)); errors.Is(err, os.ErrProcessDone) {
		return os.ErrProcessDone
	}
	err := unix.Kill(-leader.Pid, sig)
	if errors.Is(err, unix.ESRCH) {
		return os.ErrProcessDone
	}
	return err
}

// start relays signals to the process group led by leader until stop.
func (f *signalForwarder) start(leader *os.Process) {
	f.started = true
	go f.relay(leader)
}

// stop unregisters the forwarder and waits for a started relay to end, so
// no signal reaches the validator's group after RunIn returns.
func (f *signalForwarder) stop() {
	signal.Stop(f.signals)
	if f.started {
		close(f.done)
		<-f.finished
	}
}

// relay forwards the first received signal to leader's group, then
// unregisters and re-raises it on gonf (see signalForwarder). A failed
// forward (the validator already exited, or EPERM for a validator run
// through sudo/doas) changes nothing: gonf still gets its signal.
func (f *signalForwarder) relay(leader *os.Process) {
	defer close(f.finished)
	select {
	case sig := <-f.signals:
		if s, ok := sig.(syscall.Signal); ok {
			_ = signalGroup(leader, s)
		}
		signal.Stop(f.signals)
		f.reraise(sig)
	case <-f.done:
	}
}

// reraiseOnSelf sends sig to gonf's own process, where its handler (if any)
// or the default action takes over once the forwarder has unregistered.
func reraiseOnSelf(sig os.Signal) {
	if s, ok := sig.(syscall.Signal); ok {
		_ = unix.Kill(os.Getpid(), s)
	}
}
