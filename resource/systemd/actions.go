// Shared action runner: the one copy of the loop that performs a resource's
// ordered convergence actions (or only logs them in a dry run) and notes the
// result. Timer runs its systemctl actions through it, and so does the
// service policy (resource/service), for its systemctl backend and — because
// that policy is shared by every service manager — for the rcctl and
// service(8) backends too. The runner itself knows nothing about systemctl;
// only Command does. Log wording and result reporting therefore change in
// exactly one place.

package systemd

import (
	"fmt"

	"github.com/snonux/gonf/internal/logger"
	"github.com/snonux/gonf/resource"
)

// dryRunPrefix starts every dry-run log line; the Action's would text
// follows it. The wording is operator-visible and pinned by tests.
const dryRunPrefix = "dry-run: would "

// Action is one convergence step Converge performs.
type Action interface {
	// Do performs the step.
	Do() error
	// Describe returns the log text for the step: would follows
	// "dry-run: would " in a dry run, did is logged once Do succeeded.
	Describe() (would, did string)
}

// Command is the Action for one systemctl invocation: its argument vector
// after "systemctl" (build it with Args to get --user handling).
type Command []string

var _ Action = Command(nil)

// Do runs systemctl with the command's arguments (see Run for the errors).
func (c Command) Do() error { return Run(c...) }

// Describe returns the systemctl log text for the command (see Describe).
func (c Command) Describe() (would, did string) { return Describe(c) }

// Describe renders the log text of a systemctl invocation with args: would
// ("run systemctl [args]") follows "dry-run: would " in a dry run, did
// ("systemctl [args]") is logged after the command succeeded.
func Describe(args []string) (would, did string) {
	return fmt.Sprintf("run systemctl %v", args), fmt.Sprintf("systemctl %v", args)
}

// LogLines renders the complete log lines Converge emits for a: the dry-run
// line and the line logged after a succeeded. Exposed so callers' tests can
// pin their operator-visible wording without running anything.
func LogLines(a Action) (would, did string) {
	w, d := a.Describe()
	return dryRunPrefix + w, d
}

// Converge performs actions in order under resource id and notes the result.
//
//   - No actions: the resource is idle; held reports that a change gate
//     suppressed a requested action, so it is noted skipped instead of ok
//     (resource.NoteIdle).
//   - Dry run: every action is only logged ("dry-run: would ..."), none is
//     performed, and the resource is noted as would-change.
//   - Otherwise each action is performed and then logged; the resource is
//     noted changed. The first failing action aborts the rest and its error
//     is returned unwrapped; nothing is noted in that case.
func Converge(id string, actions []Action, held bool) error {
	if len(actions) == 0 {
		resource.NoteIdle(id, held)
		return nil
	}
	if resource.DryRun() {
		for _, a := range actions {
			would, _ := LogLines(a)
			logger.Info("%s", would)
		}
		resource.NoteResult(id, true)
		return nil
	}
	for _, a := range actions {
		if err := a.Do(); err != nil {
			return err
		}
		_, did := LogLines(a)
		logger.Info("%s", did)
	}
	resource.NoteResult(id, true)
	return nil
}
