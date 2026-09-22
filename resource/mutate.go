package resource

import "github.com/snonux/gonf/internal/logger"

// dryRunPrefix starts the dry-run log lines of both Mutate and Converge; the
// mutation's description follows it. Sharing it keeps the two helpers'
// operator-visible wording identical (pinned by tests). Resource kinds that
// still hand-roll their dry-run branch spell the same prefix themselves.
const dryRunPrefix = "dry-run: would "

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

// Mutate is the single place a resource's apply implementation decides
// whether a mutating syscall or exec.Command invocation actually runs.
// Individual resource kinds wrap their mutation in Mutate (or, for an ordered
// list of actions, Converge) instead of hand-rolling their own
// "if DryRun() { note; log; skip }" branch, so a NEW resource kind (or an
// edit to an existing one) cannot forget the dry-run gate on a mutation:
// Mutate provides it centrally, and the resource kinds that still hand-roll
// their own check are covered by api's TestDryRunFitness regression test
// either way.
//
// desc is a present-tense description of the mutation ("create directory
// /etc/foo", "run systemctl enable foo.timer"); it is used only for the
// dry-run log line (dryRunPrefix + desc, i.e. "dry-run: would " + desc).
// Callers keep whatever success logging they already had inside fn.
//
// When dry-run is active, Mutate never calls fn at all: the mutating
// syscall/exec.Command inside fn is skipped entirely, not just its effects.
// It records id as StatusWouldChange, logs the dry-run line, and returns
// nil.
//
// Otherwise Mutate calls fn. A nil return records id as StatusChanged. A
// non-nil return is passed through unchanged and no note is recorded,
// mirroring the ad-hoc checks this replaces (a failed mutation is not a
// completed one, so callers keep deciding their own error-path notes, if
// any).
func Mutate(id, desc string, fn func() error) error {
	if DryRun() {
		Note(id, StatusWouldChange)
		logger.Info("%s", dryRunPrefix+desc)
		return nil
	}
	if err := fn(); err != nil {
		return err
	}
	Note(id, StatusChanged)
	return nil
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
