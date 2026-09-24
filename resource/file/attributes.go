package file

// Mode and ownership: resolving the configured owner and group to ids,
// applying them to the managed regular file through an O_NOFOLLOW descriptor
// (with a guarded path-based fallback), and detecting explicitly requested
// metadata drift for EnsureFile.

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/user"
	"strconv"
	"syscall"

	"github.com/snonux/gonf/internal/logger"
)

// applyAttributesTo sets f's mode and ownership on the regular file at
// path. It opens the path with O_NOFOLLOW (plus O_NONBLOCK so a planted
// FIFO cannot hang the open) and applies the changes to the opened file
// descriptor, so the kernel refuses — ELOOP — to open a symlink planted at
// path instead of following it: a planted symlink is never followed, and a
// swap into the window between the caller's atomic rename and this open
// cannot escalate either (ELOOP, or chmod of an inode inside the already
// attacker-controlled directory). A symlink at the target path is expected
// to have been replaced by the managed regular file via the atomic write
// path.
//
// The owner is resolved via user.Lookup (name) and the group via a numeric
// parse first and user.LookupGroup (name) second, so both WithGroup("1")
// and WithGroup("daemon") work; an unresolvable group is an error.
//
// Chown runs BEFORE chmod deliberately: Linux clears the setuid/setgid bits
// on every unprivileged chown of a non-directory (even when the owner/group
// values are unchanged, which is the common case because build() defaults to
// the current user), so a chmod followed by chown would silently strip the
// special bits it had just set. Ending on the chmod means the requested
// ModeSetuid/ModeSetgid/ModeSticky flags are the final state on disk.
func (f *File) applyAttributesTo(path string) error {
	fd, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		// POSIX denies the owner O_RDONLY on a file whose mode lacks
		// owner-read (e.g. WithMode(0o000)), so a non-root run cannot open
		// its own unreadable file for the fd-based application above.
		// That is the one case where the old path-based os.Chmod worked and
		// this open does not, so fall back to the path (still refusing
		// symlinks; see applyAttributesViaPath).
		if errors.Is(err, fs.ErrPermission) {
			return f.applyAttributesViaPath(path, err)
		}
		return fmt.Errorf("failed to open %s for attribute changes: %w", path, err)
	}
	defer func() { _ = fd.Close() }()

	uid, gid, err := f.ownerIDs()
	if err != nil {
		return err
	}

	if err := fd.Chown(uid, gid); err != nil {
		return fmt.Errorf("failed to chown %s to %s:%s: %w", path, f.user, f.group, err)
	}
	logger.Debug("set owner %s:%s for %s", f.user, f.group, path)

	// Chmod last: Go maps ModeSetuid/ModeSetgid/ModeSticky to the raw
	// S_ISUID/S_ISGID/S_ISVTX syscall bits, so the full mode (including any
	// special bits normalized into f.mode) lands as the final state.
	if err := fd.Chmod(f.mode); err != nil {
		return fmt.Errorf("failed to chmod %s to %v: %w", path, f.mode, err)
	}
	logger.Debug("set mode %v for %s", f.mode, path)

	return nil
}

// ownerIDs resolves f's configured user/group into the numeric ids for the
// chown calls: -1 for unset values leaves the respective owner unchanged.
// Shared by the fd-based applyAttributesTo and its path-based fallback.
func (f *File) ownerIDs() (uid, gid int, err error) {
	uid, gid = -1, -1

	if f.user != "" {
		u, err := user.Lookup(f.user)
		if err != nil {
			return -1, -1, fmt.Errorf("failed to lookup user %s: %w", f.user, err)
		}
		parsedUID, err := strconv.Atoi(u.Uid)
		if err != nil {
			return -1, -1, fmt.Errorf("failed to parse uid %s for user %s: %w", u.Uid, f.user, err)
		}
		uid = parsedUID
	}

	if f.group != "" {
		if gid, err = resolveGroupID(f.group); err != nil {
			return -1, -1, err
		}
	}

	return uid, gid, nil
}

// applyAttributesViaPath applies f's ownership and mode to the regular file
// at path with path-based os.Chown/os.Chmod calls. It is the fallback for
// the permission-denied case of applyAttributesTo's O_NOFOLLOW open, which
// POSIX restricts to callers with owner-read access on the target mode
// (non-root cannot open its own 0o000 file, root's CAP_DAC_OVERRIDE makes
// the open succeed — so the fallback is reachable only by non-root runs).
//
// The fallback cannot widen the symlink guarantee: an Lstat first refuses
// (by returning the original open error) when a symlink sits at path, so
// the path-based calls — which would follow a final symlink — never run on
// one, and a non-regular entry is refused the same way. The residual race
// between Lstat and chmod is bounded for a non-root caller: unprivileged
// chown/chmod only succeed on entries the caller already owns, so a swap
// into that window cannot touch anything the caller does not own (the same
// exposure the pre-O_NOFOLLOW path-based implementation had on every apply).
//
// Chown runs before chmod, mirroring the fd-based path: the special bits
// must survive the chown, and the chmod is the final state.
func (f *File) applyAttributesViaPath(path string, openErr error) error {
	info, err := os.Lstat(path)
	if err != nil {
		// The entry is gone or unstatable since the open failed: surface
		// the original open error.
		return fmt.Errorf("failed to open %s for attribute changes: %w", path, openErr)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		// A symlink at the target is never followed, and a non-regular
		// entry is not what the caller verified either: surface the
		// original open error instead of path-based chown/chmod.
		return fmt.Errorf("failed to open %s for attribute changes: %w", path, openErr)
	}

	uid, gid, err := f.ownerIDs()
	if err != nil {
		return err
	}

	if err := os.Chown(path, uid, gid); err != nil {
		return fmt.Errorf("failed to chown %s to %s:%s: %w", path, f.user, f.group, err)
	}
	logger.Debug("set owner %s:%s for %s (path fallback)", f.user, f.group, path)

	if err := os.Chmod(path, f.mode); err != nil {
		return fmt.Errorf("failed to chmod %s to %v: %w", path, f.mode, err)
	}
	logger.Debug("set mode %v for %s (path fallback)", f.mode, path)

	return nil
}

// resolveGroupID resolves a configured group to a numeric gid: numeric
// strings pass through strconv.Atoi, anything else is looked up by name via
// os/user (works with and without cgo on the supported unix targets). Both
// paths wrap failures with the offending group name.
//
// The numeric path is load-bearing for options.Root, which records the group
// as "0" so the destination's kernel, not a name lookup, picks its root group
// (root on Linux, wheel on the BSDs and darwin).
func resolveGroupID(group string) (int, error) {
	gidInt, err := strconv.Atoi(group)
	if err == nil {
		if gidInt < 0 {
			// chown(uid, -1) would silently leave the group unchanged —
			// surprising for an explicitly configured group.
			return 0, fmt.Errorf("invalid gid %d for group %s", gidInt, group)
		}
		return gidInt, nil
	}
	g, lookupErr := user.LookupGroup(group)
	if lookupErr != nil {
		return 0, fmt.Errorf("failed to resolve group %s: %w", group, lookupErr)
	}
	gidInt, err = strconv.Atoi(g.Gid)
	if err != nil {
		return 0, fmt.Errorf("failed to parse gid %s for group %s: %w", g.Gid, group, err)
	}
	return gidInt, nil
}

// explicitMetadataChanged reports whether an explicitly configured mode,
// owner, or group differs from the existing regular file. Defaults selected
// by build() deliberately do not count: EnsureFile preserves pre-existing
// metadata unless the recipe requested a value.
func (f *File) explicitMetadataChanged(info fs.FileInfo) (bool, error) {
	if f.modeSet {
		const modeBits = os.ModePerm | os.ModeSetuid | os.ModeSetgid | os.ModeSticky
		if info.Mode()&modeBits != f.mode&modeBits {
			return true, nil
		}
	}
	if !f.userSet && !f.groupSet {
		return false, nil
	}

	attrs := *f
	if !attrs.userSet {
		attrs.user = ""
	}
	if !attrs.groupSet {
		attrs.group = ""
	}
	uid, gid, err := attrs.ownerIDs()
	if err != nil {
		return false, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return false, fmt.Errorf("failed to inspect ownership of %s", f.targetPath())
	}
	if uid != -1 && int(stat.Uid) != uid {
		return true, nil
	}
	if gid != -1 && int(stat.Gid) != gid {
		return true, nil
	}
	return false, nil
}
