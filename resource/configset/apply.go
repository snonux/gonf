package configset

import (
	"fmt"
	"path/filepath"

	"github.com/snonux/gonf/internal/logger"
	"github.com/snonux/gonf/resource"
	"github.com/snonux/gonf/resource/file"
)

// apply converges the set. The phases run in a fixed order and every phase
// before "publish" is free of live content writes:
//
//  1. targets/render: build every member's target and render its live bytes
//     (placeholders -> live paths);
//  2. ownership + lock (real apply only): resolve every member's owner and
//     group, so an unknown account fails before anything is staged, then take
//     the publication locks of all member directories;
//  3. diff: find members whose live content differs or is missing, and the
//     members that still have a pending marker from an earlier publication
//     that was never signalled (see marker.go);
//  4. stage + validate (only when something differs): write the COMPLETE
//     candidate set, including unchanged members, and run every validator;
//  5. repair the attributes of unchanged members in place, still before any
//     live rename, so a failure here cannot strand published members;
//  6. publish: back up, then for each differing member create its pending
//     marker and replace it, rolling back on the first failure (publish.go);
//  7. report the published and pending members, then remove their markers.
//
// Under dry-run it skips the ownership lookup and stops after the diff:
// nothing is staged and no validator runs, so a dry-run proves neither that
// the candidate set is valid nor that its owners and groups exist. Skipping
// the lookup is deliberate: a recipe may create the account earlier in the
// same run (a User before a set owned by it), and while the run is only
// previewed that account does not exist yet. A File behaves the same way.
func (s *spec) apply() error {
	s.outcomes.forget(s.name)
	targets, err := s.targets()
	if err != nil {
		return err
	}
	live, err := s.renderAll(s.liveResolver())
	if err != nil {
		return err
	}
	if resource.DryRun() {
		return s.dryRun(targets, live)
	}
	if err := s.resolveOwnership(targets); err != nil {
		return err
	}
	unlock, err := s.sys.lockDirs(s.memberDirs())
	if err != nil {
		return fmt.Errorf("config set %s: %w", s.name, err)
	}
	defer unlock()
	return s.applyLocked(targets, live)
}

// dryRun reports what an apply would publish (and what is still pending)
// without locking, staging, validating or writing anything.
func (s *spec) dryRun(targets []*file.Target, live [][]byte) error {
	changed, err := s.diff(targets, live)
	if err != nil {
		return err
	}
	pending, err := s.pendingMembers()
	if err != nil {
		return err
	}
	s.finish(changed, pendingOnly(changed, pending))
	return nil
}

// applyLocked runs the diff, validation and publication phases while the
// member directories are locked, then reports the members and removes their
// markers.
func (s *spec) applyLocked(targets []*file.Target, live [][]byte) error {
	changed, err := s.diff(targets, live)
	if err != nil {
		return err
	}
	pending, err := s.pendingMembers()
	if err != nil {
		return err
	}
	if len(changed) > 0 {
		err = s.stageValidatePublish(targets, live, changed, pending)
	} else {
		err = s.repairUnchanged(targets, changed)
	}
	if err != nil {
		return err
	}
	s.finish(changed, pendingOnly(changed, pending))
	reported := map[string]bool{}
	for key := range changed {
		reported[key] = true
	}
	for key := range pending {
		reported[key] = true
	}
	s.removeMarkers(reported)
	return nil
}

// stageValidatePublish stages and validates the complete set, repairs the
// unchanged members' attributes, then publishes the changed members. The
// staging directory is removed afterwards unless a failed rollback left
// backups in it that an operator needs.
func (s *spec) stageValidatePublish(targets []*file.Target, live [][]byte, changed, pending map[string]bool) error {
	st, err := newStage(s)
	if err != nil {
		return err
	}
	keep := false
	defer func() {
		if !keep {
			st.remove()
		}
	}()
	if err := st.validate(s); err != nil {
		return err
	}
	if err := s.repairUnchanged(targets, changed); err != nil {
		return err
	}
	pub := &publication{set: s, stage: st, targets: targets, live: live, pending: pending}
	err = pub.run(changed)
	keep = pub.keepStage
	return err
}

// repairUnchanged re-applies mode and ownership to the members that are not
// about to be replaced, like the File resource's silent metadata repair. The
// repair goes through s.sys.applyAttributes, so tests can make it fail
// deterministically.
func (s *spec) repairUnchanged(targets []*file.Target, changed map[string]bool) error {
	for i, t := range targets {
		if changed[s.members[i].key] {
			continue
		}
		if err := s.sys.applyAttributes(t); err != nil {
			return fmt.Errorf("config set %s: member %s: %w", s.name, s.members[i].key, err)
		}
	}
	return nil
}

// pendingOnly returns the members with a pending marker that are not being
// published right now: an earlier apply published them but never signalled
// them.
func pendingOnly(changed, pending map[string]bool) map[string]bool {
	out := map[string]bool{}
	for key := range pending {
		if !changed[key] {
			out[key] = true
		}
	}
	return out
}

// diff returns the keys of members whose live file would change. It reads but
// never writes, and refuses non-regular entries at member paths.
func (s *spec) diff(targets []*file.Target, live [][]byte) (map[string]bool, error) {
	changed := map[string]bool{}
	for i, t := range targets {
		differs, err := t.ContentDiffers(live[i])
		if err != nil {
			return nil, fmt.Errorf("config set %s: member %s: %w", s.name, s.members[i].key, err)
		}
		if differs {
			changed[s.members[i].key] = true
		}
	}
	return changed, nil
}

// finish notes the set and records the member outcomes for the member
// handles: a member counts as changed when it was (or would be) published, or
// is pending from an earlier apply that never signalled it. The set counts as
// changed when any member does.
func (s *spec) finish(changed, pending map[string]bool) {
	outcome := make(map[string]bool, len(s.members))
	changedAny := false
	for _, m := range s.members {
		outcome[m.key] = changed[m.key] || pending[m.key]
		changedAny = changedAny || outcome[m.key]
		switch {
		case changed[m.key] && resource.DryRun():
			logger.Info("dry-run: config set %s would publish %s", s.name, m.path)
		case changed[m.key]:
			logger.Info("config set %s: published %s", s.name, m.path)
		case pending[m.key]:
			logger.Info("config set %s: signalling the pending change of %s published by an earlier apply", s.name, m.path)
		}
	}
	resource.NoteResult(setID(s.name), changedAny)
	s.outcomes.record(s.name, outcome)
}

// renderAll renders every member's content through resolve.
func (s *spec) renderAll(resolve resolver) ([][]byte, error) {
	out := make([][]byte, len(s.members))
	for i, m := range s.members {
		rendered, err := render(m.content, resolve)
		if err != nil {
			return nil, fmt.Errorf("config set %s: member %s: %w", s.name, m.key, err)
		}
		out[i] = rendered
	}
	return out, nil
}

// liveResolver renders placeholders as the members' live paths.
func (s *spec) liveResolver() resolver {
	return s.pathResolver(func(m memberSpec) string { return m.path })
}

// pathResolver builds a resolver from a member -> absolute path mapping,
// deriving the chroot-relative form from the absolute one.
func (s *spec) pathResolver(pathOf func(memberSpec) string) resolver {
	byKey := make(map[string]memberSpec, len(s.members))
	for _, m := range s.members {
		byKey[m.key] = m
	}
	return func(ref tokenRef) (string, error) {
		m, ok := byKey[ref.key]
		if !ok {
			return "", fmt.Errorf("unknown member %q", ref.key)
		}
		path := pathOf(m)
		if !ref.chroot {
			return path, nil
		}
		if s.chroot == "" || !within(s.chroot, path) {
			return "", fmt.Errorf("member %q: %s is not inside chroot %q", ref.key, path, s.chroot)
		}
		rel, err := filepath.Rel(s.chroot, path)
		if err != nil {
			return "", err
		}
		return string(filepath.Separator) + rel, nil
	}
}
