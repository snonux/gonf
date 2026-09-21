package plan

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// SecureDir makes sure dir exists as a directory that only the calling user
// can modify, so the plan.jsonl and blobs written into it cannot be replaced
// or renamed by anyone else. It never rewrites a directory it did not create:
//
//   - a missing component (dir itself or any missing ancestor) is created 0700
//     and chmod'ed to exactly 0700 (mkdir(2) applies the umask, which could
//     otherwise leave it wider or unusably narrow); these are the only
//     directories whose mode SecureDir ever changes;
//   - a pre-existing dir is VERIFIED and left exactly as it is, mode included
//     (a 0755 checkout stays 0755): it must be a directory (not a symlink),
//     owned by the effective user, not writable by others, and writable by
//     group only when that group is the caller's user-private group (see
//     checkDirAttrs for the rule and why). A sticky directory such as /tmp is
//     refused like any other world-writable one: the sticky bit stops others
//     from deleting our entries but not from planting entries of their own
//     next to ours, and plan output should not depend on that subtlety.
//     Anything else is an error naming the directory and the problem, and
//     nothing was changed;
//   - pre-existing ancestors of dir are only traversed, not verified: they are
//     the operator's path to the directory, not somewhere gonf stores
//     anything. Every component, though, is opened relative to the descriptor
//     of its parent with O_NOFOLLOW, so a symlink anywhere in the path is
//     refused.
//
// This replaced an unconditional chmod 0700 of the final component, which
// silently changed the mode of whatever directory the operator named (the
// recipe checkout behind `gonf plan`'s default "-o .", a served directory, /tmp
// for root).
func SecureDir(dir string) error {
	fd, err := openSecureDir(dir)
	if err != nil {
		return err
	}
	return unix.Close(fd)
}

// WritePrivateFile atomically replaces name below dir with an owner-only file.
// The data is written through a fresh O_EXCL descriptor before rename, so a
// permissive pre-existing file or symlink never receives secret plan material.
func WritePrivateFile(dir, name string, data []byte) error {
	if filepath.Base(name) != name || name == "." {
		return fmt.Errorf("plan: invalid private file name %q", name)
	}
	dirFD, err := openSecureDir(dir)
	if err != nil {
		return fmt.Errorf("plan: open private directory: %w", err)
	}
	defer func() { _ = unix.Close(dirFD) }()
	tempName := "." + name + ".tmp-" + strconv.Itoa(os.Getpid())
	fileFD, err := unix.Openat(dirFD, tempName, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return fmt.Errorf("plan: create private file: %w", err)
	}
	file := os.NewFile(uintptr(fileFD), tempName)
	defer func() { _ = unix.Unlinkat(dirFD, tempName, 0) }()
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return fmt.Errorf("plan: write private file: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("plan: close private file: %w", err)
	}
	if err := unix.Renameat(dirFD, tempName, dirFD, name); err != nil {
		return fmt.Errorf("plan: replace private file: %w", err)
	}
	return nil
}

// openSecureDir walks dir component by component (see SecureDir for the
// policy) and returns a descriptor of the final directory, which the caller
// closes. Working relative to a held descriptor keeps the checks and the later
// writes on the same directory even if the path is swapped meanwhile.
func openSecureDir(dir string) (int, error) {
	base, parts := splitSecurePath(dir)
	fd, err := unix.Open(base, openDirFlags, 0)
	if err != nil {
		return -1, fmt.Errorf("open %s: %w", dirLabel(dir), err)
	}
	// created reports whether the directory fd points at was made by this call.
	// The starting point (".", "/") never was.
	created := false
	for _, part := range parts {
		next, madeHere, err := openOrCreateChild(fd, part)
		_ = unix.Close(fd)
		if err != nil {
			return -1, fmt.Errorf("open %s: component %q: %w", dirLabel(dir), part, err)
		}
		fd, created = next, madeHere
	}
	if err := finishSecureDir(fd, dir, created); err != nil {
		_ = unix.Close(fd)
		return -1, err
	}
	return fd, nil
}

// openDirFlags opens a directory without following a symlink in the last
// path component.
const openDirFlags = unix.O_RDONLY | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_CLOEXEC

// splitSecurePath cleans dir and returns the directory to start from (the
// root for an absolute path, "." otherwise) and the components below it.
// Empty and "." components are dropped, so "." and "/" have none.
func splitSecurePath(dir string) (base string, parts []string) {
	clean := filepath.Clean(dir)
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

// openOrCreateChild opens the directory name below parent, creating it 0700
// when it is missing. created is true only when this call made it: losing the
// creation race to another process (EEXIST) counts as opening a pre-existing
// directory, which the caller then treats as somebody else's. A directory made
// here is chmod'ed to exactly 0700 right away, before anything is put into it
// or below it, because the umask may have masked mkdir's mode.
func openOrCreateChild(parent int, name string) (fd int, created bool, err error) {
	fd, err = unix.Openat(parent, name, openDirFlags, 0)
	if !errors.Is(err, unix.ENOENT) {
		return fd, false, err
	}
	switch err = unix.Mkdirat(parent, name, 0o700); {
	case err == nil:
		created = true
	case !errors.Is(err, unix.EEXIST):
		return -1, false, err
	}
	if fd, err = unix.Openat(parent, name, openDirFlags, 0); err != nil {
		return -1, false, err
	}
	if created {
		if err = unix.Fchmod(fd, 0o700); err != nil {
			_ = unix.Close(fd)
			return -1, false, err
		}
	}
	return fd, created, nil
}

// finishSecureDir applies the final-component policy: a directory this call
// created is already 0700 and ours; a pre-existing one must pass the
// verification, and is never modified.
func finishSecureDir(fd int, dir string, created bool) error {
	if created {
		return nil
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return fmt.Errorf("inspect %s: %w", dirLabel(dir), err)
	}
	return checkDirAttrs(dirLabel(dir), dirAttrs{
		isDir: st.Mode&unix.S_IFMT == unix.S_IFDIR,
		uid:   st.Uid,
		gid:   st.Gid,
		mode:  uint32(st.Mode) & 0o7777,
	})
}

// dirAttrs is what the acceptance rule for an existing directory looks at,
// independent of where it was read from (fstat on our descriptor, or the
// FileInfo of a pre-check).
type dirAttrs struct {
	isDir bool
	uid   uint32
	gid   uint32
	mode  uint32 // permission bits plus setuid/setgid/sticky
}

// checkDirAttrs is the single acceptance rule for a pre-existing plan output
// directory. SecureDir (and through it WritePrivateFile and the blobs/ policy
// of Store.WriteTree/WriteGlob) and CheckExistingDir (and through it the api
// pre-check) all use it, so the up-front check cannot drift from the
// enforcement. The directory must be
//
//   - a directory, owned by the effective user (root is not exempt: a
//     directory owned by someone else lets that user swap plan.jsonl, which a
//     later root-run `gonf apply` would then trust);
//   - not writable by others, sticky bit or not;
//   - not writable by group, unless the group is the caller's user-private
//     group (isPrivateGroup).
//
// Why the private-group exception: Fedora, Ubuntu, RHEL, Rocky and most other
// Linux distributions give every user a private group (gid == uid, that user
// its only member) and set umask 002, so a fresh `git clone` or mkdir is 0775.
// Group write on such a directory lets nobody but the caller write, so
// refusing it protected nothing, yet it made the default `gonf plan -o .` fail
// in every recipe checkout. Group write for any other group (a shared 2775
// project directory, a directory whose group is not the caller's own) does let
// other users replace plan.jsonl and stays refused. The messages say why and
// what to do about it.
func checkDirAttrs(label string, a dirAttrs) error {
	if !a.isDir {
		return fmt.Errorf("%s is not a directory", label)
	}
	if euid := uint32(unix.Geteuid()); a.uid != euid {
		return fmt.Errorf("%s is owned by uid %d, not by the current user (uid %d); "+
			"refusing to store plan output where its owner can replace it: choose a directory you own (-o <private dir>)",
			label, a.uid, euid)
	}
	const remedy = "run chmod go-w on it, or choose a private directory you own (-o <private dir>)"
	switch {
	case a.mode&0o002 != 0:
		return fmt.Errorf("%s is world-writable (mode %04o); "+
			"refusing to store plan output where any user can replace it: %s", label, a.mode, remedy)
	case a.mode&0o020 != 0 && !isPrivateGroup(a.gid):
		return fmt.Errorf("%s is group-writable by group %d, which is not your private group (mode %04o); "+
			"refusing to store plan output where members of that group can replace it: %s",
			label, a.gid, a.mode, remedy)
	}
	return nil
}

// isPrivateGroup reports whether gid is the caller's user-private group by the
// standard convention: it is the caller's effective gid and that gid equals
// the effective uid. Anything else (a supplementary group, a primary group
// shared by many users as with a classic "users" group, or a process whose
// egid differs from its euid) is a group other users may belong to. The
// convention is not verified against the group database: an administrator who
// added extra members to a private group defeats it, which is their choice to
// make.
func isPrivateGroup(gid uint32) bool {
	egid := uint32(unix.Getegid())
	return gid == egid && egid == uint32(unix.Geteuid())
}

// CheckExistingDir applies SecureDir's acceptance rule to a directory that
// already exists, from its FileInfo (as os.Lstat returned, so a symlink is not
// a directory here), without opening or changing anything. It lets callers
// refuse an unusable directory early; SecureDir remains the authority, as it
// checks the descriptor it then writes through.
func CheckExistingDir(path string, info fs.FileInfo) error {
	a := dirAttrs{
		isDir: info.IsDir(),
		uid:   uint32(unix.Geteuid()),
		gid:   uint32(unix.Getegid()),
		mode:  unixModeBits(info.Mode()),
	}
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		a.uid, a.gid = st.Uid, st.Gid
	}
	return checkDirAttrs(dirLabel(path), a)
}

// unixModeBits converts Go's FileMode back to the chmod bits (permissions plus
// setuid/setgid/sticky) that error messages show, matching what fstat reports.
func unixModeBits(m fs.FileMode) uint32 {
	bits := uint32(m.Perm())
	for flag, bit := range map[fs.FileMode]uint32{fs.ModeSetuid: 0o4000, fs.ModeSetgid: 0o2000, fs.ModeSticky: 0o1000} {
		if m&flag != 0 {
			bits |= bit
		}
	}
	return bits
}

// dirLabel names dir in messages: the absolute path when it can be resolved,
// so that "." reads as the actual working directory.
func dirLabel(dir string) string {
	if abs, err := filepath.Abs(dir); err == nil {
		return abs
	}
	return dir
}
