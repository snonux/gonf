package resource

import "github.com/snonux/gonf/internal/logger"

// Action is one convergence step Converge performs. Resource kinds whose
// apply is an ordered list of external commands (Timer and Service, whose
// systemctl actions are systemd.Command values; Service's other backends and
// Package through small adapters) implement it instead of hand-rolling the
// run/log/note loop.
type Action interface {
	// Do performs the step.
	Do() error
	// Describe returns the log text for the step: would follows
	// "dry-run: would " in a dry run, did is logged once Do succeeded.
	Describe() (would, did string)
}

// Converge performs actions in order under resource id and notes the
// result. It is Mutate's counterpart for a resource whose apply is an
// ordered list of actions, and like Mutate the one place that list honours
// dry-run and is logged and noted.
//
//   - No actions: the resource is idle; held reports that a change gate
//     suppressed a requested action, so it is noted skipped instead of ok
//     (NoteIdle).
//   - Dry run: every action is only logged (LogLines' would line), none is
//     performed, and the resource is noted as would-change.
//   - Otherwise each action is performed and then logged (LogLines' did
//     line); the resource is noted changed. The first failing action aborts
//     the rest and its error is returned unwrapped; nothing is noted in that
//     case.
func Converge(id string, actions []Action, held bool) error {
	if len(actions) == 0 {
		NoteIdle(id, held)
		return nil
	}
	if DryRun() {
		for _, a := range actions {
			would, _ := LogLines(a)
			logger.Info("%s", would)
		}
		NoteResult(id, true)
		return nil
	}
	for _, a := range actions {
		if err := a.Do(); err != nil {
			return err
		}
		_, did := LogLines(a)
		logger.Info("%s", did)
	}
	NoteResult(id, true)
	return nil
}

// LogLines renders the complete log lines Converge emits for a: the dry-run
// line (dryRunPrefix + a's would text) and the line logged after a
// succeeded. Exposed so resource kinds' tests can pin their operator-visible
// wording without running anything.
func LogLines(a Action) (would, did string) {
	w, d := a.Describe()
	return dryRunPrefix + w, d
}
