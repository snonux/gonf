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

import "sync/atomic"

var active atomic.Bool

// MarkActive records that the gonf CLI is running in this process and
// returns the function that restores the previous state. internal/cli.CLI
// calls it first thing and defers the restore, so the marker is set exactly
// while CLI() runs: the elevated child (which runs the same CLI) is marked
// too, but a main that calls cli.CLI() and then api.Apply itself is not
// marked any more by the time it applies.
func MarkActive() (restore func()) { return set(true) }

// Active reports whether the gonf CLI is running in this process (MarkActive
// called and not yet restored).
func Active() bool { return active.Load() }

// SetForTest sets the marker to v and returns a function restoring the
// previous value. Tests only: api tests use it to exercise the elevated path
// with a fake runner, and to pin the refusal when the marker is unset.
func SetForTest(v bool) (restore func()) { return set(v) }

// set swaps the marker to v and returns the restore of the previous value.
func set(v bool) (restore func()) {
	old := active.Swap(v)
	return func() { active.Store(old) }
}
