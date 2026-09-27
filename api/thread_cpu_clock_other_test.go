//go:build !(linux || freebsd || openbsd || netbsd || darwin)

package api

import "time"

// threadCPUTime has no per-thread CPU clock to read on this platform, so
// sampleCost falls back to wall-clock time (see thread_cpu_clock_test.go).
func threadCPUTime() (time.Duration, bool) { return 0, false }
