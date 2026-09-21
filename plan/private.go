package plan

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"

	"github.com/snonux/gonf/internal/dirperm"
	"github.com/snonux/gonf/internal/safepath"
)

// SecureDir makes sure dir exists as a directory that only the calling user
// can modify, so the plan.jsonl and blobs written into it cannot be replaced
// or renamed by anyone else. It never rewrites a directory it did not create:
//
//   - a missing component (dir itself or any missing ancestor) is created 0700
//     and chmod'ed to exactly 0700: mkdir(2) applies the umask, which can only
//     narrow the requested 0700 (never widen it), so a restrictive umask such
//     as 0277 would leave an unusable 0500 directory that nothing can be
//     created in. These are the only directories whose mode SecureDir ever
//     changes;
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
//     of its parent with O_NOFOLLOW (the shared walk of internal/safepath),
//     so a symlink anywhere in the path is refused (and reported as a
//     symlink, not as "not a directory").
//
// SecureDir is for a directory the operator names (the plan directory);
// OpenSecureStore runs the same walk and keeps the descriptor so the blobs are
// written into exactly the directory that was verified. The blob store applies
// the same rule to blobs/ alone (openSecureChildAt). Only a path-mode store
// (NewStore: the staging store, Run's and Apply's temp dirs, all private
// directories below $TMPDIR, whose path may legitimately pass through a
// symlink such as macOS's /var -> /private/var) reaches its plan directory
// following symlinks, through openSecureChildDir.
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
// Its errors carry the package prefix "plan: " exactly once.
func WritePrivateFile(dir, name string, data []byte) error {
	if err := writePrivateFile(dir, name, data); err != nil {
		return fmt.Errorf("plan: %w", err)
	}
	return nil
}

// ReadPrivateFile reads the plan file name below dir: the read counterpart of
// WritePrivateFile, used by `gonf apply <plan.jsonl>`. name is opened without
// following a symlink and without blocking, and must be a regular file, so a
// plan.jsonl swapped for a symlink (to a file the reader may read but its
// owner never wrote as a plan) or for a FIFO is refused rather than read.
//
// It is deliberately weaker than the write side in two ways, both because the
// reader is often not the writer: dir is opened following symlinks, and
// neither dir nor the file is checked for ownership or mode. The elevated
// apply of a local run re-executes `gonf apply` as root on a plan chunk the
// unprivileged user wrote below $TMPDIR (which may be reached through a
// symlink, as /var -> /private/var on macOS), and an operator may apply a plan
// that another account recorded. Who may write the plan is decided when it is
// written (SecureDir, WritePrivateFile). Its errors carry the package prefix
// "plan: " exactly once.
func ReadPrivateFile(dir, name string) ([]byte, error) {
	path := filepath.Join(dir, name)
	data, err := readPrivateFile(dir, name, path)
	switch {
	case errors.Is(err, safepath.ErrSymlink):
		return nil, fmt.Errorf("plan: %s is a symlink; refusing to read a plan through it", path)
	case errors.Is(err, safepath.ErrNotRegular):
		return nil, fmt.Errorf("plan: %s is not a regular file", path)
	case err != nil:
		return nil, fmt.Errorf("plan: %w", err)
	}
	return data, nil
}

// ReadPrivateFilePath is ReadPrivateFile for a plan file path as an operator
// types it (`gonf apply out/plan.jsonl`). The path is split at its last
// separator BEFORE any cleaning: filepath.Dir/Base would clean "out/" into
// the directory "out" plus the name "out" and silently read out/out. A path
// whose last element is empty, "." or ".." ("out/", "out/.", ".", "/", "a/b/")
// names a directory, never a plan file, and is refused.
func ReadPrivateFilePath(path string) ([]byte, error) {
	dir, name, ok := splitFilePath(path)
	if !ok {
		return nil, fmt.Errorf("plan: %s does not name a file; a directory is not a plan file", path)
	}
	return ReadPrivateFile(dir, name)
}

// splitFilePath splits path at its last separator without cleaning it. ok is
// false when the last element does not name a file.
func splitFilePath(path string) (dir, name string, ok bool) {
	dir, name = ".", path
	if i := strings.LastIndex(path, string(filepath.Separator)); i >= 0 {
		dir, name = path[:i], path[i+1:]
		if dir == "" {
			dir = string(filepath.Separator)
		}
	}
	return dir, name, name != "" && name != "." && name != ".."
}

// readPrivateFile opens dir (following symlinks) and reads name below it
// through safepath.OpenRegularAt. path only names the file in errors.
func readPrivateFile(dir, name, path string) ([]byte, error) {
	if filepath.Base(name) != name || name == "." || name == ".." {
		return nil, fmt.Errorf("invalid private file name %q", name)
	}
	dirFD, err := safepath.OpenFollowingDir(dir)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", dir, err)
	}
	defer func() { _ = unix.Close(dirFD) }()
	file, err := safepath.OpenRegularAt(dirFD, name, path)
	if err != nil {
		if errors.Is(err, safepath.ErrSymlink) || errors.Is(err, safepath.ErrNotRegular) {
			return nil, err
		}
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(file)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return data, nil
}

// writePrivateFile is WritePrivateFile without the "plan: " prefix on its
// errors, so a caller that wraps them in an error of its own (Store.WriteFile)
// does not repeat the package prefix inside the message.
func writePrivateFile(dir, name string, data []byte) error {
	if filepath.Base(name) != name || name == "." {
		return fmt.Errorf("invalid private file name %q", name)
	}
	dirFD, err := openSecureDir(dir)
	if err != nil {
		return fmt.Errorf("open private directory: %w", err)
	}
	defer func() { _ = unix.Close(dirFD) }()
	return writePrivateFileAt(dirFD, name, data)
}

// writePrivateFileAt writes data as name (0600) inside the already verified
// directory dirFD, atomically: a temp file created O_EXCL|O_NOFOLLOW is renamed
// over name, so a reader never sees a partial file and a planted symlink is
// never written through.
func writePrivateFileAt(dirFD int, name string, data []byte) error {
	tempName := "." + name + ".tmp-" + strconv.Itoa(os.Getpid())
	fileFD, err := unix.Openat(dirFD, tempName, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return fmt.Errorf("create private file: %w", err)
	}
	file := os.NewFile(uintptr(fileFD), tempName)
	defer func() { _ = unix.Unlinkat(dirFD, tempName, 0) }()
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return fmt.Errorf("write private file: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close private file: %w", err)
	}
	if err := unix.Renameat(dirFD, tempName, dirFD, name); err != nil {
		return fmt.Errorf("replace private file: %w", err)
	}
	return nil
}

// openSecureDir walks dir component by component (see SecureDir for the
// policy) with the shared descriptor walk of internal/safepath and returns a
// descriptor of the final directory, which the caller closes. Working
// relative to a held descriptor keeps the checks and the later writes on the
// same directory even if the path is swapped meanwhile.
func openSecureDir(dir string) (int, error) {
	base, parts := safepath.Split(dir)
	baseFD, err := safepath.OpenBase(base)
	if err != nil {
		return -1, fmt.Errorf("open %s: %w", DirLabel(dir), err)
	}
	defer func() { _ = unix.Close(baseFD) }()
	fd, err := secureDirWalk(dir).OpenAt(baseFD, base, parts)
	if err != nil {
		return -1, walkError(dir, err)
	}
	return fd, nil
}

// secureDirWalk is SecureDir's policy expressed as a safepath.Walk: missing
// components are created (exactly 0700, through the mkdirChild seam), and only
// the final directory is verified, and only when it already existed
// (finishSecureDir). Components above it are merely traversed, never checked.
func secureDirWalk(dir string) safepath.Walk {
	return safepath.Walk{
		Create: true,
		Mkdir:  mkdirChild,
		Check: func(c safepath.Component) error {
			if !c.Last || c.Created {
				return nil
			}
			return finishSecureDir(c.FD, dir)
		},
	}
}

// errSymlinkComponent is how a path component that is a symlink is reported:
// safepath.ErrSymlink, whatever errno the platform gave, worded like the
// up-front refusal of the api pre-check.
var errSymlinkComponent = errors.New("is a symlink; symlinked plan directories are refused")

// componentError words a failure to open one component of dir.
func componentError(dir, part string, err error) error {
	if errors.Is(err, safepath.ErrSymlink) {
		err = errSymlinkComponent
	}
	return fmt.Errorf("open %s: component %q: %w", DirLabel(dir), part, err)
}

// walkError words an error of a secureDirWalk below dir: a component that
// could not be opened or created gets componentError, a refusal of
// finishSecureDir is already worded and returned as it is.
func walkError(dir string, err error) error {
	var ce *safepath.ComponentError
	if errors.As(err, &ce) {
		return componentError(dir, ce.Name, ce.Err)
	}
	return err
}

// mkdirChild creates a directory below an open parent. It is a variable only
// so a test can make another process "win" the creation race deterministically
// (see TestSecureDirVerifiesADirectoryItLostTheRaceFor); secureDirWalk hands
// it to safepath.OpenOrCreateDirAt.
var mkdirChild safepath.MkdirFunc = unix.Mkdirat

// openSecureChildDir is openSecureChildAt below a parent reached BY PATH, for
// the path-mode blob store (NewStore): parent (a private plan directory below
// $TMPDIR) is created when missing and opened the way os.MkdirAll and os.Open
// would, following symlinks, because $TMPDIR may be a symlinked path (macOS:
// /var -> /private/var). It must not be used for a directory the operator
// names; that one is opened by openSecureDir and kept (OpenSecureStore).
func openSecureChildDir(parent, name string) (int, error) {
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return -1, fmt.Errorf("create %s: %w", DirLabel(parent), err)
	}
	pfd, err := safepath.OpenFollowingDir(parent)
	if err != nil {
		return -1, fmt.Errorf("open %s: %w", DirLabel(parent), err)
	}
	defer func() { _ = unix.Close(pfd) }()
	return openSecureChildAt(pfd, parent, name)
}

// openSecureChildAt applies SecureDir's policy to the single directory name
// below the open directory pfd (whose path is parent, used only in messages),
// and to nothing above it, and returns a descriptor of it that the caller
// must close. name goes through the same safepath walk as SecureDir's
// components: opened O_NOFOLLOW, created exactly 0700 when missing, and
// otherwise verified by finishSecureDir (a symlink, a file, foreign
// ownership, world- or shared-group write are refused; a pre-existing
// directory is not modified). It is what the blob store uses for blobs/.
func openSecureChildAt(pfd int, parent, name string) (int, error) {
	child := filepath.Join(parent, name)
	fd, err := secureDirWalk(child).OpenAt(pfd, parent, []string{name})
	if err != nil {
		return -1, walkError(child, err)
	}
	return fd, nil
}

// finishSecureDir applies the final-component policy to a directory that
// already existed (one this walk created is 0700 and ours, and is not passed
// here): it must pass the verification, and is never modified.
func finishSecureDir(fd int, dir string) error {
	info, err := safepath.Fstat(fd)
	if err != nil {
		return fmt.Errorf("inspect %s: %w", DirLabel(dir), err)
	}
	return checkDirAttrs(DirLabel(dir), dirAttrs{
		isDir: info.IsDir(),
		uid:   info.UID,
		gid:   info.GID,
		mode:  info.Perm(),
	}, currentIDs())
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

// procIDs is the identity of the process the acceptance rule is judged for:
// its effective uid and gid. Production callers fill it from the kernel
// (currentIDs); tests pass synthetic values, so the rule can be exercised for
// any account (an ordinary user, root, a user whose egid differs from its euid)
// without changing the identity of the test process.
type procIDs struct {
	euid, egid uint32
}

// currentIDs returns the identity of the running process.
func currentIDs() procIDs {
	return procIDs{euid: uint32(unix.Geteuid()), egid: uint32(unix.Getegid())}
}

// checkDirAttrs is the single acceptance rule for a pre-existing plan output
// directory. finishSecureDir (so SecureDir, OpenSecureStore, WritePrivateFile,
// and the blobs/ policy of Store.WriteFile/WriteTree/WriteGlob through
// openSecureChildAt) and
// CheckExistingDir (and through it the api pre-check) all use it, so the
// up-front check cannot drift from the enforcement. me is the process the
// directory is judged for. The directory must be
//
//   - a directory, owned by the effective user (root is not exempt: a
//     directory owned by someone else lets that user swap plan.jsonl, which a
//     later root-run `gonf apply` would then trust);
//   - not writable by others, sticky bit or not;
//   - not writable by group, unless the group is the caller's user-private
//     group (me.isPrivateGroup).
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
func checkDirAttrs(label string, a dirAttrs, me procIDs) error {
	if !a.isDir {
		return fmt.Errorf("%s is not a directory", label)
	}
	if a.uid != me.euid {
		return fmt.Errorf("%s is owned by uid %d, not by the current user (uid %d); "+
			"refusing to store plan output where its owner can replace it: choose a directory you own (-o <private dir>)",
			label, a.uid, me.euid)
	}
	switch {
	case a.mode&0o002 != 0:
		return fmt.Errorf("%s is world-writable (mode %04o); "+
			"refusing to store plan output where any user can replace it: %s", label, a.mode, writableRemedy(a.mode))
	case a.mode&0o020 != 0 && !me.isPrivateGroup(a.gid):
		return fmt.Errorf("%s is group-writable by group %d, which is not your private group (mode %04o); "+
			"refusing to store plan output where members of that group can replace it: %s",
			label, a.gid, a.mode, writableRemedy(a.mode))
	}
	return nil
}

// writableRemedy is the advice that ends a refusal for a directory the caller
// owns that others can write. Removing group/other write with chmod is only
// good advice for an ordinary directory of the caller's. A sticky one (/tmp,
// /var/tmp: for root the owner rule passes on them too) is a shared, system
// wide scratch directory whose mode must not be touched, so the only advice
// is to pick another directory.
func writableRemedy(mode uint32) string {
	const chooseOther = "choose a private directory you own (-o <private dir>)"
	if mode&0o1000 != 0 {
		return chooseOther
	}
	return "run chmod go-w on it, or " + chooseOther
}

// isPrivateGroup reports whether gid is the caller's user-private group. It
// applies gonf's shared convention, dirperm.IDs.IsPrivateGroup (gid == egid
// == euid, and not 0), so plan output directories, file validator parents,
// the crontab lock parent and the cross-build dir all accept and refuse the
// same groups. Anything else (a supplementary group, a primary group shared
// by many users as with a classic "users" group, or a process whose egid
// differs from its euid) is a group other users may belong to. Gid 0 is never
// private: on FreeBSD, macOS and the other BSDs it is "wheel", whose
// (administrator) members could then replace plan.jsonl, so a root run
// refuses every group-writable directory; group write is not something
// root's plan directory needs.
func (p procIDs) isPrivateGroup(gid uint32) bool {
	return dirperm.IDs{EUID: p.euid, EGID: p.egid}.IsPrivateGroup(gid)
}

// CheckExistingDir applies SecureDir's acceptance rule to a directory that
// already exists, from its FileInfo (as os.Lstat returned, so a symlink is not
// a directory here), without opening or changing anything. It lets callers
// refuse an unusable directory early; SecureDir remains the authority, as it
// checks the descriptor it then writes through.
func CheckExistingDir(path string, info fs.FileInfo) error {
	me := currentIDs()
	// Without a Stat_t (a synthetic FileInfo) there is no owner to compare, so
	// the directory is taken as the caller's own.
	a := dirAttrs{isDir: info.IsDir(), uid: me.euid, gid: me.egid, mode: unixModeBits(info.Mode())}
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		a.uid, a.gid = st.Uid, st.Gid
	}
	return checkDirAttrs(DirLabel(path), a, me)
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

// DirLabel names dir in messages: the absolute path when it can be resolved,
// so that "." reads as the actual working directory.
func DirLabel(dir string) string {
	if abs, err := filepath.Abs(dir); err == nil {
		return abs
	}
	return dir
}
