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

func (f *File) validateCandidate(path string, content []byte) (err error) {
	parent := filepath.Dir(path)
	if err := verifyValidationParent(parent); err != nil {
		return fmt.Errorf("file %s: validation candidate parent: %w", path, err)
	}
	candidate, err := os.CreateTemp(parent, candidateNamePattern(path))
	if err != nil {
		return fmt.Errorf("file %s: create validation candidate: %w", path, err)
	}
	candidatePath := candidate.Name()
	closed := false
	defer func() {
		if !closed {
			if closeErr := candidate.Close(); closeErr != nil && err == nil {
				err = fmt.Errorf("file %s: close validation candidate: %w", path, closeErr)
			}
		}
		if removeErr := os.Remove(candidatePath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			cleanupErr := fmt.Errorf("file %s: remove validation candidate: %w", path, removeErr)
			if err == nil {
				err = cleanupErr
			} else {
				err = fmt.Errorf("%w; %v", err, cleanupErr)
			}
		}
	}()

	if _, err := candidate.Write(content); err != nil {
		return fmt.Errorf("file %s: write validation candidate: %w", path, err)
	}
	if err := candidate.Sync(); err != nil {
		return fmt.Errorf("file %s: sync validation candidate: %w", path, err)
	}
	if err := candidate.Close(); err != nil {
		return fmt.Errorf("file %s: close validation candidate: %w", path, err)
	}
	closed = true

	args := make([]string, len(f.validationArgs))
	for i, arg := range f.validationArgs {
		if arg == opt.CandidatePath {
			args[i] = candidatePath
			continue
		}
		args[i] = arg
	}
	if err := exec.Command(f.validationBin, args...).Run(); err != nil {
		return fmt.Errorf("file %s: validation by %s failed: %w", path, f.validationBin, err)
	}
	return nil
}

// verifyValidationParent makes the candidate pathname safe to hand to an
// external validator after the file has been closed. CreateTemp's 0600 mode
// protects its bytes, but an untrusted writer of the parent could unlink and
// replace the candidate between Close and exec. A safe single-file contract
// therefore needs a real parent owned by the applying uid and not writable by
// its group or other users. Normal system configuration parents (/etc,
// /var/nsd/etc, ...) satisfy this; callers needing a shared writable staging
// area need a separate multi-file/staging design instead.
func verifyValidationParent(parent string) error {
	currentUID := uint32(os.Geteuid())
	if !filepath.IsAbs(parent) {
		return fmt.Errorf("%s is not an absolute path", parent)
	}
	components, err := validationPathComponents(parent)
	if err != nil {
		return err
	}
	root := filepath.VolumeName(parent) + string(filepath.Separator)
	if len(components) == 0 {
		info, err := os.Lstat(root)
		if err != nil {
			return fmt.Errorf("inspect %s: %w", root, err)
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return fmt.Errorf("cannot verify ownership of %s", root)
		}
		if st.Uid != currentUID {
			return fmt.Errorf("%s is not owned by the applying uid", root)
		}
		if info.Mode().Perm()&0o022 != 0 {
			return fmt.Errorf("%s is writable by group or other users", root)
		}
		return nil
	}
	current := root
	for i, component := range components {
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if err != nil {
			return fmt.Errorf("inspect %s: %w", current, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%s contains a symlink path component", current)
		}
		if !info.IsDir() {
			return fmt.Errorf("%s is not a directory", current)
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return fmt.Errorf("cannot verify ownership of %s", current)
		}

		if i == len(components)-1 {
			if st.Uid != currentUID {
				return fmt.Errorf("%s is not owned by the applying uid", current)
			}
			if info.Mode().Perm()&0o022 != 0 {
				return fmt.Errorf("%s is writable by group or other users", current)
			}
		} else {
			if st.Uid != 0 && st.Uid != currentUID {
				return fmt.Errorf("%s is owned by an untrusted uid", current)
			}
			if info.Mode().Perm()&0o022 != 0 {
				if info.Mode()&os.ModeSticky == 0 {
					return fmt.Errorf("%s is writable by group or other users without sticky protection", current)
				}
			}
		}
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
