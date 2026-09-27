//go:build !(linux || freebsd || openbsd || darwin)

package api

import "time"

// threadCPUTime has no per-thread CPU clock to read on this platform, so
// timedSample falls back to wall-clock time (see thread_cpu_clock_test.go).
func threadCPUTime() (time.Duration, bool) { return 0, false }
