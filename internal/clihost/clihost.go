// Package clihost records whether this process runs gonf's command line
// (internal/cli.CLI, reached through cli.CLI from cmd/gonf and client main
// packages).
//
// It exists for the local elevated re-exec. A privileged apply chunk is run
// as `<os.Executable()> apply <chunk>` through sudo/doas, which only works
// when that executable's main hands its arguments to gonf's CLI. A program
// that calls api.Run or api.Apply from its own main instead would re-run
// that main as root, which applies the same resources again and, finding
// the same elevated ops, re-execs itself again. api refuses the elevated
// re-exec up front unless Active reports true, so such a program fails
// loudly before anything is applied.
//
// The marker lives in an internal package so only gonf's own CLI can set it.
package clihost

import (
	"sync"
	"sync/atomic"
)

// depth counts the CLI() calls currently running in this process. A counter
// rather than a flag, because CLI() calls may overlap (nested, or from
// several goroutines): with a swap-and-restore flag, the call that started
// first but returned first would restore "unset" while the other still ran.
var depth atomic.Int64

// MarkActive records that one more gonf CLI call is running in this process
// and returns the function that ends that mark (idempotent: a second call
// does nothing). internal/cli.CLI calls it first thing and defers the
// release, so the marker is set exactly while at least one CLI() runs: the
// elevated child (which runs the same CLI) is marked too, but a main that
// calls cli.CLI() and then api.Apply itself is not marked any more by the
// time it applies.
func MarkActive() (release func()) {
	depth.Add(1)
	var once sync.Once
	return func() { once.Do(func() { depth.Add(-1) }) }
}

// Active reports whether a gonf CLI call is running in this process (some
// MarkActive not yet released).
func Active() bool { return depth.Load() > 0 }

// SetForTest forces the marker to v (one running CLI call, or none) and
// returns a function restoring the previous count. Tests only: api tests use
// it to exercise the elevated path with a fake runner, and to pin the
// refusal when the marker is unset. It must not overlap real MarkActive
// calls, whose releases it would otherwise miscount.
func SetForTest(v bool) (restore func()) {
	var n int64
	if v {
		n = 1
	}
	old := depth.Swap(n)
	return func() { depth.Store(old) }
}
