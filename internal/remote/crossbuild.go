package remote

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"github.com/snonux/gonf/internal/dirperm"
	"github.com/snonux/gonf/internal/logger"
)

// Where cross-compiled gonf binaries live before they are scp'd.
//
// Each Pusher (in production: each gonf process, via defaultPusher) builds
// into its OWN private directory, created on first use with
// os.MkdirTemp(<parent>, "gonf-cross-*"): a random name, mode 0700, owned by
// the invoking user. Each platform is normally built once per Pusher (the
// in-memory buildCache) and reused for every host of that platform in the
// run; it is rebuilt only when a reuse check below fails or after Close
// dropped the cache. Nothing is shared or reused across processes, so
// concurrent gonf runs (or concurrent `go test` processes) can never see,
// overwrite or delete each other's builds.
//
// The parent is CrossBuildRoot, or os.TempDir() when that is empty. It is
// made absolute and then symlinks are resolved (e.g. macOS /tmp ->
// /private/tmp, a symlinked $TMPDIR or $PWD): checkBuildParent checks the
// RESOLVED path, and the build dir is created under it, so a later change of
// a symlink cannot redirect it. The resolved parent AND every ancestor up to
// / must be owned by the effective uid or root; world write is allowed only
// with the sticky bit, and group write only with the sticky bit or when the
// group is the caller's user-private group (checkBuildParentInfo) —
// otherwise another user could rename a directory on the path away and put
// their own in its place. So /tmp (root, 1777), macOS
// /private/var/folders/.../T and home directories that are 0755, 0700 or
// umask-002 0775 with a private group pass; a directory owned by another
// user (including the unmapped uid 65534 "nobody" of files from outside a
// rootless container's user namespace), or shared-group or world writable
// without the sticky bit, anywhere on the path is refused.
//
// Trust is never based on a path alone. The directory's identity
// (os.FileInfo) is recorded at creation. Before the directory is reused
// (for any platform), after every build into it, and before a cached binary
// is handed out, verifyBuildDir re-checks that the path is still that same
// real directory (Lstat: not a symlink; os.SameFile; owned by the effective
// uid) with mode 0700, and a cached binary must additionally be a regular
// file owned by us whose SHA-256 still matches the one recorded right after
// the build (verifyBinary). If anything fails — e.g. a temp sweeper removed
// the directory and another local user planted a same-named directory or
// symlink — nothing is reused: the state is dropped with a warning, and a
// NEW directory is created and the binary rebuilt.
//
// Cleanup: Close removes the directories (the CLI calls CleanupBuilds when
// it returns; gonf library code never ends the process, so that deferred
// call always runs on a normal or error return). A directory is only removed while it is still ours by identity (same inode,
// real directory, owned by us — see isOurDir), so a planted replacement or a
// symlink's target is never touched, while one of ours whose mode was merely
// loosened is still cleaned up. A crash, SIGKILL, an uncaught signal such as
// SIGHUP/SIGQUIT, or an api-only program that never calls CleanupBuilds can
// leave one gonf-cross-* dir behind for the OS temp cleaner.
//
// History: the cache used to be a FIXED path, $TMPDIR/gonf-cross-<goos>-
// <goarch>/gonf, shared by every process. Concurrent processes overwrote
// each other's binary (one could push another's build), test cleanup deleted
// the shared dir under other test processes, and in world-writable /tmp
// another local user could pre-create the dir and swap the binary.

// buildDirState is one private build directory and the identity it had
// when this Pusher created it.
type buildDirState struct {
	path string
	info os.FileInfo // Lstat result right after creation
}

// cachedBuild is one built binary: where it is, which build dir holds it,
// and its SHA-256 recorded right after the build.
type cachedBuild struct {
	path string
	dir  *buildDirState
	sum  string
}

// buildGonf cross-compiles gonf for goos/goarch into p's private build dir
// and returns the binary's path, reusing this Pusher's earlier build of the
// same platform only while it still verifies (see the file comment).
func (p *Pusher) buildGonf(ctx context.Context, goos, goarch string) (string, error) {
	key := goos + "/" + goarch

	// Serialize only same-key builds (see buildKeyLock's doc comment):
	// unrelated goos/goarch pairs never wait on each other here.
	keyMu := p.buildKeyLock(key)
	keyMu.Lock()
	defer keyMu.Unlock()

	if c, ok := p.buildCacheGet(key); ok {
		err := verifyBuildDir(c.dir)
		if err == nil {
			err = verifyBinary(c.path, c.sum)
		}
		if err == nil {
			return c.path, nil
		}
		logger.Warn("gonf cross-build for %s not reused: %v; rebuilding in a new private dir", key, err)
		p.retireBuildDir(c.dir)
	}

	dir, err := p.privateBuildDir()
	if err != nil {
		return "", err
	}
	return p.buildInto(ctx, dir, key, goos, goarch)
}

// buildInto runs the build into dir, re-verifies the binary AND the dir
// (which could have been swapped while the build ran), and records the
// result in the cache.
func (p *Pusher) buildInto(ctx context.Context, dir *buildDirState, key, goos, goarch string) (string, error) {
	// goos/goarch are sanitized (sanitizeID, from remote.go) before use in
	// a file name: PushTarget.GOOS/GOARCH can be set directly by a caller,
	// so they are not guaranteed to be path-safe.
	out := filepath.Join(dir.path, "gonf-"+sanitizeID(goos)+"-"+sanitizeID(goarch))
	if err := p.GoBuildRunner(ctx, goos, goarch, out, gonfCmdPackage); err != nil {
		// Never leave a partial binary behind.
		_ = os.Remove(out)
		return "", err
	}
	sum, err := checkOwnRegularFile(out)
	if err == nil {
		err = verifyBuildDir(dir)
	}
	if err != nil {
		// Only clean up inside a dir that is still ours; a swapped-in
		// replacement is not ours to modify.
		if isOurDir(dir) {
			_ = os.Remove(out)
		}
		p.retireBuildDir(dir)
		return "", err
	}
	p.buildCacheSet(key, cachedBuild{path: out, dir: dir, sum: sum})
	return out, nil
}

// privateBuildDir returns p's current build dir after re-verifying it, or
// creates a new one when there is none or the old one no longer verifies.
func (p *Pusher) privateBuildDir() (*buildDirState, error) {
	p.buildMu.Lock()
	defer p.buildMu.Unlock()
	if d := p.buildDir; d != nil {
		err := verifyBuildDir(d)
		if err == nil {
			return d, nil
		}
		logger.Warn("gonf cross-build dir not reused: %v; creating a new one", err)
		p.retireLocked(d)
	}
	d, err := newBuildDir(p.CrossBuildRoot)
	if err != nil {
		return nil, err
	}
	p.buildDir = d
	return d, nil
}

// retireBuildDir is retireLocked with buildMu taken.
func (p *Pusher) retireBuildDir(d *buildDirState) {
	p.buildMu.Lock()
	defer p.buildMu.Unlock()
	p.retireLocked(d)
}

// retireLocked stops reusing d: it is no longer the current dir and every
// cached binary inside it is forgotten. A dir that is still ours by
// identity (isOurDir: e.g. only its mode was loosened) is kept for Close to
// remove, since another platform's build may still be using it; a dir that
// is no longer ours (replaced or turned into a symlink) is never deleted by
// us, so it is simply forgotten. buildMu must be held.
func (p *Pusher) retireLocked(d *buildDirState) {
	if d == nil {
		return
	}
	if p.buildDir == d {
		p.buildDir = nil
	}
	for key, c := range p.buildCache {
		if c.dir == d {
			delete(p.buildCache, key)
		}
	}
	if !isOurDir(d) {
		return
	}
	for _, r := range p.retiredDirs {
		if r == d {
			return
		}
	}
	p.retiredDirs = append(p.retiredDirs, d)
}

// Close removes p's private build dirs (those still ours by identity) and
// forgets its cached builds. Call it once every push using p has finished; a
// later build simply creates a fresh dir.
func (p *Pusher) Close() error {
	p.buildMu.Lock()
	dirs := p.retiredDirs
	if p.buildDir != nil {
		dirs = append(dirs, p.buildDir)
	}
	p.buildDir, p.retiredDirs = nil, nil
	p.buildCache = map[string]cachedBuild{}
	p.buildMu.Unlock()

	var errs error
	for _, d := range dirs {
		errs = errors.Join(errs, removeBuildDir(d))
	}
	return errs
}

// CleanupBuilds removes the cross-build dir of the package's default
// Pusher (the one EnsureRemoteGonf uses). internal/cli.CLI defers it, which
// covers the gonf binary and every config program that runs through
// cli.CLI; a program pushing via the api package without cli.CLI leaves the
// dir to the OS temp cleaner.
func CleanupBuilds() error {
	return defaultPusher.Close()
}

// newBuildDir creates a private build dir under the resolved, checked
// parent (root, or os.TempDir() when empty), records its identity, checks
// it came out private (a restrictive umask or a filesystem ignoring modes
// could make it otherwise).
func newBuildDir(root string) (*buildDirState, error) {
	parent := root
	if parent == "" {
		parent = os.TempDir()
	}
	resolved, err := checkBuildParent(parent)
	if err != nil {
		return nil, err
	}
	path, err := os.MkdirTemp(resolved, "gonf-cross-*")
	if err != nil {
		return nil, fmt.Errorf("create gonf cross-build dir: %w; %s", err, tmpdirHint)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("gonf cross-build dir: %w; %s", err, tmpdirHint)
	}
	d := &buildDirState{path: path, info: info}
	if err := verifyBuildDir(d); err != nil {
		// The dir is ours, only its mode is wrong: that comes from the
		// umask (or a filesystem ignoring modes), not from TMPDIR.
		_ = os.Remove(path)
		return nil, fmt.Errorf("%w; check your umask (it must leave the owner rwx, e.g. 022 or 077)", err)
	}
	return d, nil
}

// tmpdirHint is appended to every error about where the build dir lives,
// telling the user how to point gonf at a safe location instead. The export
// form works in sh-family shells and fish; csh/tcsh users set it with
// setenv instead.
const tmpdirHint = `create a private directory (mkdir -p -m 700 "$HOME/tmp") and export TMPDIR=$HOME/tmp before running gonf`

// checkBuildParent makes parent absolute, resolves symlinks, checks the
// resolved directory and every ancestor up to the root with
// checkBuildParentInfo, and returns the resolved path.
func checkBuildParent(parent string) (string, error) {
	return checkBuildParentAs(parent, dirperm.Current(), os.Lstat)
}

// checkBuildParentAs is checkBuildParent for the given ids and lstat
// function (parameters so a test can simulate a foreign-owned ancestor
// without root).
func checkBuildParentAs(parent string, ids dirperm.IDs, lstat func(string) (os.FileInfo, error)) (string, error) {
	// Abs first: EvalSymlinks on a relative path would leave it relative,
	// and Abs would then prepend a $PWD that may itself contain symlinks.
	abs, err := filepath.Abs(parent)
	if err != nil {
		return "", fmt.Errorf("gonf cross-build parent dir: %w; %s", err, tmpdirHint)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("gonf cross-build parent dir: %w; %s", err, tmpdirHint)
	}
	for dir := resolved; ; dir = filepath.Dir(dir) {
		fi, err := lstat(dir)
		if err != nil {
			return "", fmt.Errorf("gonf cross-build parent dir: %w; %s", err, tmpdirHint)
		}
		if err := checkBuildParentInfo(dir, fi, ids); err != nil {
			return "", err
		}
		if filepath.Dir(dir) == dir {
			return resolved, nil
		}
	}
}

// checkBuildParentInfo refuses a directory on the build dir's path that
//   - is a symlink or not a directory,
//   - is owned by anyone but the effective uid or root (its owner could
//     rename what is below it away),
//   - is world-writable without the sticky bit, or
//   - is group-writable without the sticky bit, unless the group is the
//     caller's user-private group (dirperm.IDs.IsPrivateGroup), whose only
//     member is the caller — the umask-002 layout of Fedora and most Linux
//     distributions, accepted the same way by plan output dirs and the
//     crontab lock parent.
func checkBuildParentInfo(dir string, fi os.FileInfo, ids dirperm.IDs) error {
	if fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("gonf cross-build parent path %s is a symlink; %s", dir, tmpdirHint)
	}
	if !fi.IsDir() {
		return fmt.Errorf("gonf cross-build parent path %s is not a directory; %s", dir, tmpdirHint)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || (st.Uid != ids.EUID && st.Uid != 0) {
		return fmt.Errorf("gonf cross-build parent path %s is not owned by uid %d or root, so its owner could replace gonf's build dir; %s", dir, ids.EUID, tmpdirHint)
	}
	perm, sticky := fi.Mode().Perm(), fi.Mode()&os.ModeSticky != 0
	switch {
	case perm&0o002 != 0 && !sticky:
		return fmt.Errorf("gonf cross-build parent path %s (mode %04o) is world-writable without the sticky bit, so other users could replace gonf's build dir; %s", dir, perm, tmpdirHint)
	case perm&0o020 != 0 && !sticky && !ids.IsPrivateGroup(st.Gid):
		return fmt.Errorf("gonf cross-build parent path %s (mode %04o) is writable by group %d, which is not your private group, without the sticky bit, so its members could replace gonf's build dir; %s", dir, perm, st.Gid, tmpdirHint)
	}
	return nil
}

// checkDirIdentity checks that d.path is still the directory this Pusher
// created: a real directory (Lstat, so a symlink fails), the same inode
// (os.SameFile) and owned by the effective uid. It returns the fresh
// FileInfo for further checks.
func checkDirIdentity(d *buildDirState) (os.FileInfo, error) {
	return checkDirIdentityAs(d, os.Geteuid())
}

// checkDirIdentityAs is checkDirIdentity for owner uid euid (a parameter so
// a test can exercise the foreign-owner case without root).
func checkDirIdentityAs(d *buildDirState, euid int) (os.FileInfo, error) {
	if d == nil {
		return nil, errors.New("no build dir")
	}
	fi, err := os.Lstat(d.path)
	if err != nil {
		return nil, fmt.Errorf("gonf cross-build dir: %w", err)
	}
	switch {
	case !fi.IsDir():
		return nil, fmt.Errorf("gonf cross-build dir %s is no longer a real directory (symlink or other file)", d.path)
	case !os.SameFile(d.info, fi):
		return nil, fmt.Errorf("gonf cross-build dir %s was replaced by another directory", d.path)
	case !ownedBy(fi, euid):
		return nil, fmt.Errorf("gonf cross-build dir %s is not owned by uid %d", d.path, euid)
	}
	return fi, nil
}

// isOurDir reports whether d.path is still, by identity, the directory this
// Pusher created (checkDirIdentity), regardless of its mode.
func isOurDir(d *buildDirState) bool {
	_, err := checkDirIdentity(d)
	return err == nil
}

// verifyBuildDir checks that d.path is still our directory by identity and
// still private (mode 0700), i.e. safe to build into or reuse from.
func verifyBuildDir(d *buildDirState) error {
	fi, err := checkDirIdentity(d)
	if err != nil {
		return err
	}
	if fi.Mode().Perm() != 0o700 {
		return fmt.Errorf("gonf cross-build dir %s has mode %04o, want 0700", d.path, fi.Mode().Perm())
	}
	return nil
}

// verifyBinary checks that path is a regular file owned by us whose SHA-256
// is still wantSum.
func verifyBinary(path, wantSum string) error {
	sum, err := checkOwnRegularFile(path)
	if err != nil {
		return err
	}
	if sum != wantSum {
		return fmt.Errorf("gonf cross-build binary %s changed since it was built", path)
	}
	return nil
}

// checkOwnRegularFile requires path to be a regular file (not a symlink)
// owned by the effective uid and returns its hex SHA-256. The checks and
// the hash use one descriptor opened with O_NOFOLLOW (O_NONBLOCK so a
// planted FIFO cannot hang us), so they describe the same file.
func checkOwnRegularFile(path string) (string, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return "", fmt.Errorf("gonf cross-build binary: %w", err)
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf("gonf cross-build binary: %w", err)
	}
	if !fi.Mode().IsRegular() || !ownedByUs(fi) {
		return "", fmt.Errorf("gonf cross-build binary %s is not a regular file owned by uid %d", path, os.Geteuid())
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("hash gonf cross-build binary %s: %w", path, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// ownedByUs reports whether fi belongs to the effective uid.
func ownedByUs(fi os.FileInfo) bool {
	return ownedBy(fi, os.Geteuid())
}

// ownedBy reports whether fi belongs to uid.
func ownedBy(fi os.FileInfo, uid int) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == uid
}

// removeBuildDir removes d only while it is still ours by identity
// (isOurDir), so a planted replacement or a symlink's target is never
// touched.
func removeBuildDir(d *buildDirState) error {
	if !isOurDir(d) {
		return nil
	}
	return os.RemoveAll(d.path)
}
