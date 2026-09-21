package configset

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/snonux/gonf/internal/logger"
	"github.com/snonux/gonf/internal/validator"
	"github.com/snonux/gonf/resource/file"
)

// stagePrefix starts every staging directory name; the set name follows so a
// leftover directory can be attributed to its set.
const stagePrefix = ".gonf-configset-"

// stage is one apply's private staging directory. root is created with
// os.MkdirTemp (mode 0700, unpredictable name) inside the verified staging
// parent. Below it, candidates mirrors the members' layout below the parent,
// so relative references between members resolve the same way staged as
// live, and backups holds the hard links (or copies) publish takes of the
// members it replaces. Keeping them in separate subdirectories means no
// member path can collide with a backup.
type stage struct {
	parent     string
	root       string
	candidates string
	backups    string
	paths      map[string]string // member key -> staged candidate path
}

// newStage verifies the staging parent, creates the private staging directory
// and writes the complete candidate set into it. Any failure removes what was
// created; no live file is touched.
func newStage(s *spec) (*stage, error) {
	parent := s.stagingParent()
	if err := file.VerifyStagingParent(parent); err != nil {
		return nil, fmt.Errorf("config set %s: staging directory %s: %w", s.name, parent, err)
	}
	warnLeftoverStages(parent, s.name)
	root, err := os.MkdirTemp(parent, stagePrefix+s.name+"+*")
	if err != nil {
		return nil, fmt.Errorf("config set %s: create staging directory: %w", s.name, err)
	}
	st := &stage{
		parent: parent, root: root,
		candidates: filepath.Join(root, "candidates"), backups: filepath.Join(root, "backups"),
		paths: map[string]string{},
	}
	if err := st.writeCandidates(s); err != nil {
		st.remove()
		return nil, err
	}
	return st, nil
}

// writeCandidates writes every member, rendered with staged paths, as a 0600
// file at its mirrored location.
func (st *stage) writeCandidates(s *spec) error {
	for _, m := range s.members {
		rel, err := filepath.Rel(st.parent, m.path)
		if err != nil {
			return fmt.Errorf("config set %s: member %s: %w", s.name, m.key, err)
		}
		st.paths[m.key] = filepath.Join(st.candidates, rel)
	}
	staged, err := s.renderAll(s.pathResolver(func(m memberSpec) string { return st.paths[m.key] }))
	if err != nil {
		return err
	}
	for i, m := range s.members {
		if err := writePrivate(st.paths[m.key], staged[i]); err != nil {
			return fmt.Errorf("config set %s: stage member %s: %w", s.name, m.key, err)
		}
	}
	return nil
}

// writePrivate creates path exclusively (0600, parents 0700) and syncs it.
// O_EXCL guarantees the candidate is a file this apply created, never an
// existing entry.
func writePrivate(path string, content []byte) (err error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := f.Close(); err == nil {
			err = cerr
		}
	}()
	if _, err := f.Write(content); err != nil {
		return err
	}
	return f.Sync()
}

// validate runs every validator in order with placeholders rendered as staged
// paths and the working directory set to the candidate mirror of the staging
// parent. Each one goes through the shared internal/validator runner, exactly
// like File's WithValidation: argv only (no shell), stdin from /dev/null,
// bounded by the command timeout (-cmd-timeout), and a failure carries a
// capped, sanitized copy of the validator's output. The first failure aborts.
func (st *stage) validate(s *spec) error {
	resolve := s.pathResolver(func(m memberSpec) string { return st.paths[m.key] })
	for i, v := range s.validators {
		args, err := renderArgs(v.Args, resolve)
		if err != nil {
			return fmt.Errorf("config set %s: validator %d (%s): %w", s.name, i+1, v.Bin, err)
		}
		if err := runValidator(st.candidates, v.Bin, args); err != nil {
			return fmt.Errorf("config set %s: validation by %s failed, nothing published: %w", s.name, v.Bin, err)
		}
		logger.Debug("config set %s: validator %s accepted the staged set", s.name, v.Bin)
	}
	return nil
}

// runValidator is internal/validator.RunIn; a variable only so tests can
// check which working directory and argv the set passes. Production code
// never reassigns it.
var runValidator = validator.RunIn

// remove deletes the staging directory including any backups in it.
func (st *stage) remove() {
	if err := os.RemoveAll(st.root); err != nil {
		logger.Warn("config set: remove staging directory %s: %v", st.root, err)
	}
}

// warnLeftoverStages reports staging directories of this set that a previous
// apply left behind: it was killed mid-apply, or a rollback failed and kept
// its backups. They are never reused and never deleted automatically,
// because they may hold the only copy of a replaced configuration; the
// current apply converges forward from the live state regardless.
func warnLeftoverStages(parent, name string) {
	for _, leftover := range leftoverStages(parent, name) {
		if _, err := os.Lstat(filepath.Join(leftover, "backups")); errors.Is(err, os.ErrNotExist) {
			logger.Warn("config set %s: leftover staging directory %s from an interrupted apply (no backups); remove it", name, leftover)
			continue
		}
		logger.Warn("config set %s: leftover staging directory %s holds backups of an interrupted publication; inspect and remove it", name, leftover)
	}
}

// leftoverStages lists set name's staging directories in parent. The "+"
// after the name cannot occur in a set name, so the set "a" never claims
// the directories of a set "a.b" or "a-b".
func leftoverStages(parent, name string) []string {
	entries, err := os.ReadDir(parent)
	if err != nil {
		return nil
	}
	prefix := stagePrefix + name + "+"
	var out []string
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), prefix) {
			out = append(out, filepath.Join(parent, e.Name()))
		}
	}
	return out
}
