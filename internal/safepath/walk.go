package safepath

import (
	"path/filepath"

	"golang.org/x/sys/unix"
)

// Component describes one directory of a walk, as it is handed to
// Walk.Check right after it was opened.
type Component struct {
	// FD is the open descriptor of the component. It is only valid during the
	// Check call; the walk owns it.
	FD int
	// Path is the walk's base path joined with every component up to and
	// including this one (the base itself when the walk has no components).
	Path string
	// Last is true for the final directory of the walk.
	Last bool
	// Created is true when this walk made the directory (Walk.Create).
	Created bool
}

// ComponentError is a failure to open (or create) one component of a walk.
// Err is the cause: ErrSymlink, ErrInvalidComponent, or the kernel's error
// (unix.ENOENT, unix.ENOTDIR for a non-directory, unix.EACCES, ...), so that
// callers can word each case their own way with errors.Is. Refusals by
// Walk.Check are returned as they are, never wrapped in a ComponentError.
type ComponentError struct {
	Path string // the component's path, as in Component.Path
	Name string // the component's own name (the base, for the base itself)
	Err  error
}

func (e *ComponentError) Error() string { return "open " + e.Path + ": " + e.Err.Error() }

// Unwrap returns the cause.
func (e *ComponentError) Unwrap() error { return e.Err }

// Walk opens a directory path component by component (see the package
// documentation for the guarantee). Its zero value opens existing directories
// only and verifies nothing; the fields add a caller's policy.
type Walk struct {
	// Create makes missing components with OpenOrCreateDirAt: mode exactly
	// 0700, reported to Check as Created.
	Create bool
	// Mkdir is the mkdirat used by Create (unix.Mkdirat when nil).
	Mkdir MkdirFunc
	// Check, when set, is called for every component right after it was
	// opened, top-down, so the first refused component is the one reported
	// and nothing below it is opened. When the walk has no components the
	// base itself is passed, as the last component. With components, the base
	// is never passed: it is the caller's starting point.
	Check func(Component) error
}

// Open opens base with OpenBase, walks parts below it (see OpenAt) and
// returns a descriptor of the final directory, which the caller closes. A
// failure to open base is a *ComponentError whose Path and Name are base.
func (w Walk) Open(base string, parts []string) (int, error) {
	baseFD, err := OpenBase(base)
	if err != nil {
		return -1, &ComponentError{Path: base, Name: base, Err: err}
	}
	defer func() { _ = unix.Close(baseFD) }()
	return w.OpenAt(baseFD, base, parts)
}

// OpenAt walks parts below the directory parent (whose path is parentPath,
// only used to name components) and returns a new descriptor of the final
// directory, which the caller closes. parent stays open and owned by the
// caller; how the caller reached it is its own business (the blob store opens
// the plan directory following symlinks). Without parts the result is a new
// descriptor of parent itself, checked as the last component.
func (w Walk) OpenAt(parent int, parentPath string, parts []string) (int, error) {
	if len(parts) == 0 {
		fd, err := unix.Openat(parent, ".", DirFlags, 0)
		if err != nil {
			return -1, &ComponentError{Path: parentPath, Name: ".", Err: err}
		}
		return w.check(Component{FD: fd, Path: parentPath, Last: true})
	}
	fd, path := parent, parentPath
	for i, name := range parts {
		path = filepath.Join(path, name)
		next, created, err := w.openChild(fd, name)
		if fd != parent {
			_ = unix.Close(fd)
		}
		if err != nil {
			return -1, &ComponentError{Path: path, Name: name, Err: err}
		}
		if fd, err = w.check(Component{FD: next, Path: path, Last: i == len(parts)-1, Created: created}); err != nil {
			return -1, err
		}
	}
	return fd, nil
}

// openChild opens (Create: or creates) the component name below fd.
func (w Walk) openChild(fd int, name string) (int, bool, error) {
	if w.Create {
		return OpenOrCreateDirAt(fd, name, w.Mkdir)
	}
	next, err := OpenDirAt(fd, name)
	return next, false, err
}

// check runs w.Check on c and returns c.FD, or closes it when Check refuses.
func (w Walk) check(c Component) (int, error) {
	if w.Check == nil {
		return c.FD, nil
	}
	if err := w.Check(c); err != nil {
		_ = unix.Close(c.FD)
		return -1, err
	}
	return c.FD, nil
}
