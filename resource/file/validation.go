package file

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/snonux/gonf/internal/safepath"
	"github.com/snonux/gonf/resource"
	opt "github.com/snonux/gonf/resource/options"
)

// ensureValidatedFile validates content from a private, unique candidate
// before the normal single-file reconciliation can replace the live target.
// A candidate is created beside the target, so validators that resolve simple
// relative paths from the configuration's parent keep that context. This
// deliberately does not try to model chroots or multi-file include sets.
func (f *File) ensureValidatedFile(path string, content []byte) error {
	if !f.validationSet || resource.DryRun() {
		return f.ensureFile(path, content)
	}
	if err := f.validateCandidate(path, content); err != nil {
		return err
	}
	return f.ensureFile(path, content)
}

// validateCandidate stages content in a private candidate beside path, runs
// the configured validator on it and always removes the candidate again. The
// steps run in a fixed order: parent safety check, CreateTemp, write/sync/
// close, validator exec, cleanup. A cleanup failure is reported on its own
// when everything else succeeded, or appended to the earlier error otherwise.
func (f *File) validateCandidate(path string, content []byte) (err error) {
	parent := filepath.Dir(path)
	if err := verifyValidationParent(parent); err != nil {
		return fmt.Errorf("file %s: validation candidate parent: %w", path, err)
	}
	candidate, err := createValidationCandidate(parent, candidateNamePattern(path))
	if err != nil {
		return fmt.Errorf("file %s: create validation candidate: %w", path, err)
	}
	candidatePath := candidate.Name()
	// closed is only set once writeValidationCandidate has closed the file
	// successfully. Until then the deferred cleanup closes it, which also
	// covers early error returns and panics between CreateTemp and Close.
	closed := false
	defer func() { err = cleanupValidationCandidate(path, candidate, closed, err) }()

	if err := writeValidationCandidate(path, candidate, content); err != nil {
		return err
	}
	closed = true
	return f.runValidation(path, candidatePath)
}

// validationCandidateFile is the part of *os.File that staging a candidate
// needs. It lets tests inject write/sync/close failures and panics.
type validationCandidateFile interface {
	Name() string
	Write([]byte) (int, error)
	Sync() error
	Close() error
}

// createValidationCandidate creates the private (0600) candidate file. It is
// a variable only so tests can wrap the real file with failure injection to
// check validateCandidate's close/remove wiring; production code never
// reassigns it.
var createValidationCandidate = func(dir, pattern string) (validationCandidateFile, error) {
	candidate, err := os.CreateTemp(dir, pattern)
	if err != nil {
		// Return an untyped nil, never a nil *os.File wrapped in the interface.
		return nil, err
	}
	return candidate, nil
}

// writeValidationCandidate writes, fsyncs and closes the candidate so the
// validator sees the complete bytes. It returns at the first failure without
// closing; the caller's deferred cleanupValidationCandidate closes the file
// in that case.
func writeValidationCandidate(path string, candidate validationCandidateFile, content []byte) error {
	if _, err := candidate.Write(content); err != nil {
		return fmt.Errorf("file %s: write validation candidate: %w", path, err)
	}
	if err := candidate.Sync(); err != nil {
		return fmt.Errorf("file %s: sync validation candidate: %w", path, err)
	}
	if err := candidate.Close(); err != nil {
		return fmt.Errorf("file %s: close validation candidate: %w", path, err)
	}
	return nil
}

// cleanupValidationCandidate closes the candidate unless it was already
// closed successfully, then unlinks it. A close error only becomes the result
// when nothing failed earlier; otherwise the earlier error is kept (a failed
// Close in writeValidationCandidate leads to a second Close here whose
// "already closed" error is dropped for that reason).
func cleanupValidationCandidate(path string, candidate validationCandidateFile, closed bool, err error) error {
	if !closed {
		if closeErr := candidate.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("file %s: close validation candidate: %w", path, closeErr)
		}
	}
	return removeValidationCandidate(path, candidate.Name(), err)
}

// removeValidationCandidate unlinks the candidate and folds a removal failure
// into err. An already-missing candidate is fine (e.g. the validator deleted
// it). When err is already set it stays the wrapped, primary error and the
// cleanup failure is appended as text only.
func removeValidationCandidate(path, candidatePath string, err error) error {
	removeErr := os.Remove(candidatePath)
	if removeErr == nil || errors.Is(removeErr, os.ErrNotExist) {
		return err
	}
	cleanupErr := fmt.Errorf("file %s: remove validation candidate: %w", path, removeErr)
	if err == nil {
		return cleanupErr
	}
	return fmt.Errorf("%w; %v", err, cleanupErr)
}

// runValidation executes the validator directly (no shell) with the
// CandidatePath placeholder replaced by the staged candidate's path.
func (f *File) runValidation(path, candidatePath string) error {
	args := substituteCandidatePath(f.validationArgs, candidatePath)
	if err := exec.Command(f.validationBin, args...).Run(); err != nil {
		return fmt.Errorf("file %s: validation by %s failed: %w", path, f.validationBin, err)
	}
	return nil
}

// substituteCandidatePath returns a copy of args in which every element equal
// to opt.CandidatePath is replaced by candidatePath. Only whole-element
// matches are replaced; the caller's slice is never modified.
func substituteCandidatePath(args []string, candidatePath string) []string {
	out := make([]string, len(args))
	for i, arg := range args {
		if arg == opt.CandidatePath {
			out[i] = candidatePath
			continue
		}
		out[i] = arg
	}
	return out
}

// validationFstat and validationGeteuid are the only identity and attribute
// inputs of the candidate parent checks: the walk itself always opens the
// real directories, but what each opened directory is judged by comes from
// validationFstat. They are variables so tests can feed synthetic owners and
// modes (foreign uids, a writable root, ...) that do not depend on the host
// or on the ancestry of $TMPDIR. Production code never reassigns them.
var (
	validationFstat   = func(fd int, _ string) (safepath.Info, error) { return safepath.Fstat(fd) }
	validationGeteuid = os.Geteuid
)

// verifyValidationParent makes the candidate pathname safe to hand to an
// external validator after the file has been closed. CreateTemp's 0600 mode
// protects its bytes, but an untrusted writer of the parent could unlink and
// replace the candidate between Close and exec. A safe single-file contract
// therefore needs a real parent owned by the applying uid and not writable by
// its group or other users. Normal system configuration parents (/etc,
// /var/nsd/etc, ...) satisfy this; callers needing a shared writable staging
// area need a separate multi-file/staging design instead.
//
// The path is walked from "/" with the shared descriptor walk of
// internal/safepath (every component opened O_NOFOLLOW relative to its
// parent's descriptor, nothing created), and each component is judged on the
// descriptor that was opened, so what is checked is exactly the directory the
// walk continues in, not a name that may have been swapped since an lstat.
// Components are checked top-down, so the first unsafe ancestor is the one
// reported and nothing below it is opened. The root itself is only checked
// when it is the parent (see verifyValidationComponent). The final descriptor
// is closed again: CreateTemp and the validator work by path, which is sound
// because the rules above leave nobody but root and the applying uid able to
// rename or replace anything along it.
func verifyValidationParent(parent string) error {
	currentUID := uint32(validationGeteuid())
	if !filepath.IsAbs(parent) {
		return fmt.Errorf("%s is not an absolute path", parent)
	}
	components, err := validationPathComponents(parent)
	if err != nil {
		return err
	}
	root := filepath.VolumeName(parent) + string(filepath.Separator)
	walk := safepath.Walk{Check: func(c safepath.Component) error {
		return verifyValidationComponent(c, currentUID)
	}}
	fd, err := walk.Open(root, components)
	if err != nil {
		return validationWalkError(err)
	}
	return unix.Close(fd)
}

// validationWalkError words a component the walk could not open. A refusal
// of verifyValidationComponent is already worded and returned as it is.
func validationWalkError(err error) error {
	var ce *safepath.ComponentError
	if !errors.As(err, &ce) {
		return err
	}
	switch {
	case errors.Is(ce.Err, safepath.ErrSymlink):
		return fmt.Errorf("%s contains a symlink path component", ce.Path)
	case errors.Is(ce.Err, unix.ENOTDIR):
		return fmt.Errorf("%s is not a directory", ce.Path)
	}
	return fmt.Errorf("inspect %s: %w", ce.Path, ce)
}

// verifyValidationComponent checks one directory on the path to the candidate
// parent, as the walk opened it: it is a real directory already (the walk
// opens components O_DIRECTORY|O_NOFOLLOW and refuses symlinks and other
// files). The last one directly holds the candidate and must be private to the
// applying uid; intermediates only need to stop others from renaming what lies
// below them. When the parent is the filesystem root itself, the root is the
// last (and only) component and gets the private rule.
func verifyValidationComponent(c safepath.Component, currentUID uint32) error {
	info, err := validationFstat(c.FD, c.Path)
	if err != nil {
		return fmt.Errorf("inspect %s: %w", c.Path, err)
	}
	if c.Last {
		return verifyValidationPrivateDir(c.Path, info, currentUID)
	}
	return verifyValidationIntermediate(c.Path, info, currentUID)
}

// verifyValidationPrivateDir requires the directory holding the candidate to
// be owned by the applying uid and not writable by group or other users.
// Unlike for intermediates, the sticky bit does not relax this rule.
func verifyValidationPrivateDir(path string, info safepath.Info, currentUID uint32) error {
	if info.UID != currentUID {
		return fmt.Errorf("%s is not owned by the applying uid", path)
	}
	if info.Perm()&0o022 != 0 {
		return fmt.Errorf("%s is writable by group or other users", path)
	}
	return nil
}

// verifyValidationIntermediate accepts an ancestor owned by root or the
// applying uid. It may be group/other-writable only with the sticky bit (as
// /tmp is), which stops other users from renaming or replacing our subtree.
func verifyValidationIntermediate(path string, info safepath.Info, currentUID uint32) error {
	if info.UID != 0 && info.UID != currentUID {
		return fmt.Errorf("%s is owned by an untrusted uid", path)
	}
	if info.Perm()&0o022 != 0 && info.Perm()&0o1000 == 0 {
		return fmt.Errorf("%s is writable by group or other users without sticky protection", path)
	}
	return nil
}

// validationPathComponents returns the raw directory components without
// resolving them through filepath.Clean. Rejecting ".." (which the safepath
// walk would otherwise follow to the parent) and refusing a symlink at every
// component prevents a symlink/parent alias from making the path checked here
// differ from the path used by CreateTemp and the validator.
func validationPathComponents(path string) ([]string, error) {
	volume := filepath.VolumeName(path)
	rest := strings.TrimPrefix(path, volume)
	rest = strings.TrimPrefix(rest, string(filepath.Separator))
	var components []string
	for _, component := range strings.Split(rest, string(filepath.Separator)) {
		switch component {
		case "", ".":
			continue
		case "..":
			return nil, fmt.Errorf("%s contains a parent-directory path component", path)
		default:
			components = append(components, component)
		}
	}
	return components, nil
}

func candidateNamePattern(path string) string {
	const (
		marker  = ".gonfvalidate"
		maxRand = 10
		nameMax = 255
	)
	base := filepath.Base(path)
	if max := nameMax - len(marker) - maxRand; len(base) > max {
		base = base[:max]
	}
	return base + marker + "*"
}
