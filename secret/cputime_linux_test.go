//go:build linux

package secret

import (
	"runtime"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// measureCost runs f and returns the CPU time its OS thread spent on it
// (user plus system, RUSAGE_THREAD), not the wall-clock time. The goroutine
// is locked to its thread for the duration, so no other goroutine runs on
// that thread and the figure is f's own work: time the scheduler spent
// running other processes or other tests' goroutines (the machine load that
// made task 2h2's wall-clock deadlines flaky) is not counted. GC assists f
// itself triggers are counted, which is intended, since they scale with f's
// own allocation. A getrusage failure (not expected on Linux) fails the
// test rather than silently returning a meaningless figure.
func measureCost(t *testing.T, f func()) time.Duration {
	t.Helper()
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	before, err := threadCPUTime()
	if err != nil {
		t.Fatalf("getrusage(RUSAGE_THREAD): %v", err)
	}
	f()
	after, err := threadCPUTime()
	if err != nil {
		t.Fatalf("getrusage(RUSAGE_THREAD): %v", err)
	}
	return after - before
}

// threadCPUTime returns the calling OS thread's consumed CPU time.
func threadCPUTime() (time.Duration, error) {
	var ru unix.Rusage
	if err := unix.Getrusage(unix.RUSAGE_THREAD, &ru); err != nil {
		return 0, err
	}
	return time.Duration(ru.Utime.Nano() + ru.Stime.Nano()), nil
}
