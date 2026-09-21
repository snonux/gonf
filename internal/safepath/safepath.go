// Package safepath is gonf's single implementation of symlink-safe path
// traversal. A path is opened one component at a time, each component
// relative to the descriptor of its already opened parent and with
// O_NOFOLLOW, so no component is ever resolved through a symlink, and every
// check a caller makes on a component applies to the very directory the next
// component (and the caller's later reads or writes) is opened in, even if the
// path is renamed or swapped meanwhile. A pathname walk with lstat(2) cannot
// give that guarantee: the name it checked may be a different object by the
// time it is used.
//
// The package provides the mechanism only; each caller states its own policy
// on top of it (what to create, what to verify, how to word a refusal):
//
//   - plan.SecureDir and plan.WritePrivateFile walk the whole plan directory
//     from "/" or ".", create missing components 0700 and verify only the
//     final directory (Walk with Create and a Check on the last component);
//   - the plan blob store walks only blobs/ (Walk.OpenAt), below either the
//     descriptor plan.OpenSecureStore kept from SecureDir's walk (the plan
//     directory an operator names) or a private $TMPDIR plan directory opened
//     following symlinks ($TMPDIR may be a symlinked path), and writes tree
//     blobs below blobs/ with Walk.OpenAt and Create too;
//   - plan.ReadPrivateFile opens the plan file with OpenRegularAt below a
//     directory reached the normal way;
//   - api secrets walk the directories below secrets/ without creating or
//     verifying anything and require the secret to be a regular file
//     (Walk.Open + OpenRegularAt);
//   - resource/file walks the parent of a validation candidate from "/",
//     creating nothing and verifying every component with its own ownership
//     and mode rule (Walk with a Check on every component).
package safepath

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// ErrSymlink reports a path component that is a symlink. The kernel reports
// an O_NOFOLLOW open of one differently per platform (ENOTDIR or ELOOP on
// Linux, EMLINK on FreeBSD, EFTYPE on NetBSD, ...), and ENOTDIR also means "a
// regular file", so every open failure is diagnosed with fstatat and a
// symlink is always reported as this one error.
var ErrSymlink = errors.New("is a symlink")

// ErrNotRegular reports that OpenRegularAt found something other than a
// regular file (a directory, a FIFO, a device, a socket).
var ErrNotRegular = errors.New("is not a regular file")

// ErrInvalidComponent reports a component that is not exactly one path
// component ("", ".", or a name containing a separator), and a base that is
// not a single component. Opening such a name would resolve more than the one
// component O_NOFOLLOW protects, so it is refused instead.
//
// ".." is allowed: it names the parent of the held descriptor, which is never
// a symlink, so it keeps the guarantee (a relative "-o ../out" needs it). It
// does leave the subtree below the base, so a caller that must stay inside
// its base (secrets/) refuses ".." before walking.
var ErrInvalidComponent = errors.New("is not a single path component")

// DirFlags opens a directory for traversal without following a symlink in the
// component opened. searchFlag (per platform, see search_*.go) makes the open
// need only search permission where the platform allows it, as an lstat(2)
// walk of the same path would: O_PATH on Linux, O_SEARCH on FreeBSD, and
// O_RDONLY (read permission needed too) elsewhere; SearchOnly says which.
const DirFlags = searchFlag | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_CLOEXEC

// readDirFlags opens a directory readable, for the one operation a search-only
// descriptor cannot do: fchmod of a directory OpenOrCreateDirAt just created
// (it is ours and at most 0700, so the caller can read it unless the umask
// removed the owner's read bit).
const readDirFlags = unix.O_RDONLY | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_CLOEXEC

// followDirFlags is DirFlags without O_NOFOLLOW, for OpenFollowingDir.
const followDirFlags = searchFlag | unix.O_DIRECTORY | unix.O_CLOEXEC

// fileFlags opens a file without following a symlink and without blocking on
// a FIFO (or a device) that was planted where a regular file is expected.
const fileFlags = unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_NONBLOCK | unix.O_CLOEXEC

// Info is what fstat(2) reports about an opened component: the file type, the
// permission bits (with setuid, setgid and sticky) and the owner.
type Info struct {
	Mode uint32 // st_mode: file type (S_IFMT) plus permission bits
	UID  uint32
	GID  uint32
}

// IsDir reports whether the component is a directory.
func (i Info) IsDir() bool { return i.Mode&unix.S_IFMT == unix.S_IFDIR }

// IsRegular reports whether the component is a regular file.
func (i Info) IsRegular() bool { return i.Mode&unix.S_IFMT == unix.S_IFREG }

// Perm returns the chmod bits: permissions plus setuid, setgid and sticky.
func (i Info) Perm() uint32 { return i.Mode & 0o7777 }

// Fstat returns the Info of an open descriptor.
func Fstat(fd int) (Info, error) {
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return Info{}, err
	}
	// st.Mode is uint16 on the BSDs and macOS, uint32 on Linux.
	return Info{Mode: uint32(st.Mode), UID: st.Uid, GID: st.Gid}, nil
}

// OpenBase opens the starting directory of a walk: "/", "." or a single
// component relative to the working directory (e.g. "secrets"), without
// following it if it is a symlink. Anything longer is refused, because
// O_NOFOLLOW would only protect its last component; walk the rest with
// Walk.OpenAt instead.
func OpenBase(base string) (int, error) {
	if base != "/" && base != "." && !validName(base) {
		return -1, ErrInvalidComponent
	}
	fd, err := unix.Open(base, DirFlags, 0)
	if err != nil {
		return -1, diagnose(unix.AT_FDCWD, base, err)
	}
	return fd, nil
}

// OpenDirAt opens the directory name below parent without following a
// symlink. A symlink is reported as ErrSymlink; a regular file keeps the
// kernel's ENOTDIR.
func OpenDirAt(parent int, name string) (int, error) {
	if !validName(name) {
		return -1, ErrInvalidComponent
	}
	fd, err := unix.Openat(parent, name, DirFlags, 0)
	if err != nil {
		return -1, diagnose(parent, name, err)
	}
	return fd, nil
}

// MkdirFunc creates the directory name below dirfd with mode (as
// unix.Mkdirat does). It is injectable so tests can let another process win
// the creation race deterministically.
type MkdirFunc func(dirfd int, name string, mode uint32) error

// OpenOrCreateDirAt opens the directory name below parent, creating it 0700
// with mkdir (unix.Mkdirat when nil) when it is missing. created is true only
// when this call made it: losing the creation race to another process
// (EEXIST) counts as opening a pre-existing directory, which the caller must
// then treat as somebody else's (verify it, never chmod it). A directory made
// here is chmod'ed to exactly 0700 right away, before anything is put into it
// or below it, because the umask may have narrowed mkdir's mode (never widened
// it), and a restrictive umask such as 0277 would otherwise leave an unusable
// 0500 directory.
func OpenOrCreateDirAt(parent int, name string, mkdir MkdirFunc) (fd int, created bool, err error) {
	if mkdir == nil {
		mkdir = unix.Mkdirat
	}
	fd, err = OpenDirAt(parent, name)
	if !errors.Is(err, unix.ENOENT) {
		return fd, false, err
	}
	switch err = mkdir(parent, name, 0o700); {
	case err == nil:
		created = true
	case !errors.Is(err, unix.EEXIST):
		return -1, false, err
	}
	if !created {
		fd, err = OpenDirAt(parent, name)
		return fd, false, err
	}
	return openCreatedDirAt(parent, name)
}

// openCreatedDirAt opens a directory OpenOrCreateDirAt has just made, readable
// (a search-only descriptor cannot be fchmod'ed) and still O_NOFOLLOW, and
// chmods it to exactly 0700.
func openCreatedDirAt(parent int, name string) (int, bool, error) {
	fd, err := unix.Openat(parent, name, readDirFlags, 0)
	if err != nil {
		return -1, false, diagnose(parent, name, err)
	}
	if err := unix.Fchmod(fd, 0o700); err != nil {
		_ = unix.Close(fd)
		return -1, false, err
	}
	return fd, true, nil
}

// OpenFollowingDir opens dir following symlinks anywhere in it, as os.Open
// would, but needing only search permission where DirFlags does. It is for a
// directory whose path is the caller's own business (the blob store's plan
// directory, the directory of a plan file to read), below which the walk or
// OpenRegularAt then takes over.
func OpenFollowingDir(dir string) (int, error) {
	return unix.Open(dir, followDirFlags, 0)
}

// OpenRegularAt opens name below parent for reading, without following a
// symlink and without blocking on a FIFO, and returns it only when it is a
// regular file (ErrNotRegular otherwise). path is only the name the returned
// *os.File carries.
func OpenRegularAt(parent int, name, path string) (*os.File, error) {
	if !validName(name) {
		return nil, ErrInvalidComponent
	}
	fd, err := unix.Openat(parent, name, fileFlags, 0)
	if err != nil {
		return nil, diagnose(parent, name, err)
	}
	info, err := Fstat(fd)
	if err == nil && !info.IsRegular() {
		err = ErrNotRegular
	}
	if err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}

// diagnose turns the error of a failed O_NOFOLLOW open of name below parent
// into ErrSymlink when name is a symlink, and leaves every other error (a
// missing name, a regular file where a directory was asked for, a permission
// problem) as it is. The fstatat only chooses the wording: whatever name is
// by now, the open itself did not follow anything.
func diagnose(parent int, name string, err error) error {
	if errors.Is(err, unix.ENOENT) {
		return err
	}
	var st unix.Stat_t
	if unix.Fstatat(parent, name, &st, unix.AT_SYMLINK_NOFOLLOW) == nil && st.Mode&unix.S_IFMT == unix.S_IFLNK {
		return ErrSymlink
	}
	return err
}

// validName reports whether name is exactly one path component other than
// ".": not empty and without a separator (see ErrInvalidComponent for "..").
func validName(name string) bool {
	return name != "" && name != "." && !strings.ContainsRune(name, filepath.Separator)
}

// Split cleans path and returns the base to start a walk from ("/" for an
// absolute path, "." otherwise) and the components below it. filepath.Clean
// resolves ".." lexically, so only leading ".." components of a relative path
// remain (walked as the parent of the held descriptor), and empty and "."
// components are dropped, so "." and "/" have none. A caller that must refuse
// ".." instead of resolving it (the validation parent) splits the path itself.
func Split(path string) (base string, parts []string) {
	clean := filepath.Clean(path)
	base = "."
	if filepath.IsAbs(clean) {
		base = string(filepath.Separator)
		clean = strings.TrimPrefix(clean, base)
	}
	for _, part := range strings.Split(clean, string(filepath.Separator)) {
		if part != "" && part != "." {
			parts = append(parts, part)
		}
	}
	return base, parts
}
