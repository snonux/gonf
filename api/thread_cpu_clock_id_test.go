//go:build linux || freebsd || openbsd || darwin

package api

import "golang.org/x/sys/unix"

// threadCPUClockID is the per-thread CPU-time clock threadCPUTime reads.
const threadCPUClockID = unix.CLOCK_THREAD_CPUTIME_ID
