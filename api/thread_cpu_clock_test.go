//go:build linux || freebsd || openbsd || darwin

package api

import (
	"time"

	"golang.org/x/sys/unix"
)

// threadCPUTime returns the CPU time the calling OS thread has consumed so
// far (CLOCK_THREAD_CPUTIME_ID), and false if the clock cannot be read. The
// caller must have locked its goroutine to the thread (runtime.LockOSThread)
// for the difference of two readings to mean anything; timedSample does.
func threadCPUTime() (time.Duration, bool) {
	var ts unix.Timespec
	if err := unix.ClockGettime(unix.CLOCK_THREAD_CPUTIME_ID, &ts); err != nil {
		return 0, false
	}
	return time.Duration(ts.Nano()), true
}
