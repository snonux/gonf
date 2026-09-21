package file

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

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

// validationLstat and validationGeteuid are the only filesystem and identity
// inputs of the candidate parent checks. They are variables so tests can feed
// synthetic owners and modes (foreign uids, a writable root, ...) that do not
// depend on the host or on the ancestry of $TMPDIR. Production code never
// reassigns them.
var (
	validationLstat   = os.Lstat
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
// Components are checked top-down so the first unsafe ancestor is the one
// reported.
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
	if len(components) == 0 {
		return verifyValidationRoot(root, currentUID)
	}
	current := root
	for i, component := range components {
		current = filepath.Join(current, component)
		last := i == len(components)-1
		if err := verifyValidationComponent(current, last, currentUID); err != nil {
			return err
		}
	}
	return nil
}

// verifyValidationRoot handles a candidate parent that is the filesystem root
// itself. The root is then the directory holding the candidate, so it gets the
// same private-directory rule as a last component. The root is always a real
// directory, so only its ownership and mode are checked.
func verifyValidationRoot(root string, currentUID uint32) error {
	info, err := validationLstat(root)
	if err != nil {
		return fmt.Errorf("inspect %s: %w", root, err)
	}
	st, err := validationStat(root, info)
	if err != nil {
		return err
	}
	return verifyValidationPrivateDir(root, info, st, currentUID)
}

// verifyValidationComponent checks one directory on the path to the candidate
// parent. Every component must be a real (non-symlink) directory. The last
// one directly holds the candidate and must be private to the applying uid;
// intermediates only need to stop others from renaming what lies below them.
func verifyValidationComponent(current string, last bool, currentUID uint32) error {
	info, err := validationLstat(current)
	if err != nil {
		return fmt.Errorf("inspect %s: %w", current, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s contains a symlink path component", current)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", current)
	}
	st, err := validationStat(current, info)
	if err != nil {
		return err
	}
	if last {
		return verifyValidationPrivateDir(current, info, st, currentUID)
	}
	return verifyValidationIntermediate(current, info, st, currentUID)
}

// validationStat exposes the raw owner information needed for the uid checks.
func validationStat(path string, info os.FileInfo) (*syscall.Stat_t, error) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return nil, fmt.Errorf("cannot verify ownership of %s", path)
	}
	return st, nil
}

// verifyValidationPrivateDir requires the directory holding the candidate to
// be owned by the applying uid and not writable by group or other users.
// Unlike for intermediates, the sticky bit does not relax this rule.
func verifyValidationPrivateDir(path string, info os.FileInfo, st *syscall.Stat_t, currentUID uint32) error {
	if st.Uid != currentUID {
		return fmt.Errorf("%s is not owned by the applying uid", path)
	}
	if info.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("%s is writable by group or other users", path)
	}
	return nil
}

// verifyValidationIntermediate accepts an ancestor owned by root or the
// applying uid. It may be group/other-writable only with the sticky bit (as
// /tmp is), which stops other users from renaming or replacing our subtree.
func verifyValidationIntermediate(path string, info os.FileInfo, st *syscall.Stat_t, currentUID uint32) error {
	if st.Uid != 0 && st.Uid != currentUID {
		return fmt.Errorf("%s is owned by an untrusted uid", path)
	}
	if info.Mode().Perm()&0o022 != 0 && info.Mode()&os.ModeSticky == 0 {
		return fmt.Errorf("%s is writable by group or other users without sticky protection", path)
	}
	return nil
}

// validationPathComponents returns the raw directory components without
// resolving them through filepath.Clean. Rejecting ".." and checking every
// component with Lstat prevents a symlink/parent alias from making the path
// checked here differ from the path used by CreateTemp and the validator.
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
