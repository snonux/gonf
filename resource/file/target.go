package file

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"path/filepath"

	"github.com/snonux/gonf/internal/safepath"
	opt "github.com/snonux/gonf/resource/options"
	"golang.org/x/sys/unix"
)

// Target is a built but unregistered content-managed regular file. Composite
// resources (resource/configset) use it to publish several files under their
// own validation, locking and rollback policy while reusing exactly the File
// resource's write path: the same option defaults (mode 0640, applying user
// and group unless WithOwner/WithGroup say otherwise), the same private
// CreateTemp + fsync + rename atomic write, and the same O_NOFOLLOW
// attribute application. A Target never records a report note; its caller
// decides which resource IDs a publication is reported under.
type Target struct {
	f *File
}

// NewTarget builds a Target for the absolute path from file options. Only
// placement and attribute options make sense here (WithMode, WithOwner,
// WithGroup): content is passed to each call instead, so a composite resource
// can compare and write the bytes it rendered once. Content, line-edit,
// template, validation and absence options are refused because they would
// silently be ignored.
func NewTarget(path string, opts ...opt.FileOption) (*Target, error) {
	f, err := build(path, opts...)
	if err != nil {
		return nil, err
	}
	if f.contentSet || f.lineEdit() || f.Absent || f.validationSet || f.template || f.templateDataSet {
		return nil, fmt.Errorf("file %s: a publication target only accepts mode and ownership options", path)
	}
	return &Target{f: f}, nil
}

// ResolveOwnership resolves the configured owner and group to numeric ids
// without changing anything. Composite resources call it for every member
// before their first live write, so an unknown user or group fails the whole
// publication up front instead of after some members were already replaced.
func (t *Target) ResolveOwnership() error {
	_, _, err := t.f.ownerIDs()
	return err
}

// ContentDiffers reports whether publishing content would change the live
// file: true when the path (or its directory) is missing or holds other
// bytes. Unlike the File resource, which replaces any non-regular entry, a
// Target refuses one (a symlink, FIFO, socket, device or directory at a
// managed configuration path is an operator decision a multi-file
// publication must not overrule). The file is opened through
// internal/safepath: no component of the path is followed through a symlink,
// the open never blocks on a FIFO, and the bytes compared are read from the
// very descriptor whose type was checked, so nothing swapped in between a
// check and the read can be followed or block the apply.
func (t *Target) ContentDiffers(content []byte) (bool, error) {
	live, err := readRegularNoFollow(t.f.path)
	if errors.Is(err, unix.ENOENT) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return sha256.Sum256(live) != sha256.Sum256(content), nil
}

// readRegularNoFollow reads the regular file at the absolute path through a
// safepath walk of its directory plus OpenRegularAt. A missing directory or
// file returns an error wrapping unix.ENOENT.
func readRegularNoFollow(path string) ([]byte, error) {
	base, parts := safepath.Split(filepath.Dir(path))
	dirfd, err := safepath.Walk{}.Open(base, parts)
	if errors.Is(err, unix.ENOENT) {
		return nil, err
	}
	var ce *safepath.ComponentError
	if errors.As(err, &ce) && (errors.Is(ce.Err, safepath.ErrSymlink) || errors.Is(ce.Err, unix.ENOTDIR)) {
		return nil, fmt.Errorf("open %s: %s is not a real directory (symlink or file in the path)", path, ce.Path)
	}
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = unix.Close(dirfd) }()
	f, err := safepath.OpenRegularAt(dirfd, filepath.Base(path), path)
	switch {
	case errors.Is(err, unix.ENOENT):
		return nil, err
	case errors.Is(err, safepath.ErrSymlink), errors.Is(err, safepath.ErrNotRegular):
		return nil, fmt.Errorf("%s is not a regular file (%v); refusing to replace it", path, err)
	case err != nil:
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return data, nil
}

// Write atomically replaces the live file with content (private temp file
// beside it, fsync, rename, directory fsync) and then applies the configured
// ownership and mode to the new inode, exactly like a changed File.
func (t *Target) Write(content []byte) error {
	if err := atomicWrite(t.f.path, content, t.f.mode); err != nil {
		return err
	}
	return t.f.applyAttributesTo(t.f.path)
}

// ApplyAttributes re-applies the configured ownership and mode to the live
// file in place. It is the unchanged-content counterpart of Write, matching
// the File resource's silent metadata repair.
func (t *Target) ApplyAttributes() error {
	return t.f.applyAttributesTo(t.f.path)
}

// ReadSource reads a controller-side source file exactly like a File's
// WithSource: a FIFO, socket, device or directory is refused (a symlink to a
// regular file is followed, as for File), and the read never blocks on a
// planted FIFO.
func ReadSource(path string) ([]byte, error) {
	return readForSource(path)
}

// VerifyStagingParent applies the WithValidation candidate-parent contract to
// a directory that will hold a private staging directory: every component is
// a real directory (no symlinks, no ".."), intermediates are owned by root or
// the applying uid and are not writable by others unless sticky, and dir
// itself is owned by the applying uid and not group/other writable. That is
// what keeps a staged candidate set from being swapped between its creation
// and the validator exec.
func VerifyStagingParent(dir string) error {
	return verifyValidationParent(dir)
}
