package resource

import "github.com/snonux/gonf/internal/logger"

// dryRunPrefix starts the dry-run log lines of both Mutate and Converge; the
// mutation's description follows it. Sharing it keeps the two helpers'
// operator-visible wording identical (pinned by tests). Resource kinds that
// still hand-roll their dry-run branch spell the same prefix themselves.
const dryRunPrefix = "dry-run: would "

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
