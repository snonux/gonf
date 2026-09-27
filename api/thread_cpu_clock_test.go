//go:build linux || freebsd || openbsd || netbsd || darwin

package api

import (
	"time"

	"golang.org/x/sys/unix"
)

// threadCPUTime returns the CPU time the calling OS thread has consumed so
// far (the threadCPUClockID clock), and false if the clock cannot be read --
// for instance EINVAL from a kernel that does not know the clock id -- in
// which case sampleCost falls back to wall-clock time. The caller must have
// locked its goroutine to the thread (runtime.LockOSThread) for the
// difference of two readings to mean anything; sampleCost does.
func threadCPUTime() (time.Duration, bool) {
	var ts unix.Timespec
	if err := unix.ClockGettime(threadCPUClockID, &ts); err != nil {
		return 0, false
	}
	return time.Duration(ts.Nano()), true
}
