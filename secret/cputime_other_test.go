//go:build !linux

package secret

import (
	"testing"
	"time"
)

// measureCost runs f and returns its wall-clock duration. It is the
// portable fallback for the Linux helper of the same name, which measures
// f's own thread CPU time instead: there is no portable per-thread CPU
// clock, so here machine load inflates the figure. Callers compare two
// measurements taken back to back (a cost ratio), never an absolute
// deadline, so load that is steady across both still largely cancels out.
func measureCost(t *testing.T, f func()) time.Duration {
	t.Helper()
	start := time.Now()
	f()
	return time.Since(start)
}
