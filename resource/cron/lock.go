package cron

// Crontab write lock.
//
// crontab(1) has no compare-and-swap write, so Cron.apply holds an advisory
// flock(2) across its read/merge/write transaction; two Gonf processes that
// update the same crontab then cannot discard each other's blocks.
//
// Where the lock lives is a security decision. It used to be
// /tmp/gonf-crontab-<target uid>: a predictable name in a world-writable
// directory. Any local user could pre-create it (mkdir -m 0700) and every
// later apply failed its ownership check, and an unprivileged run that
// targeted another account leaked the directory it could not chown, bricking
// that account's cron management until someone removed it by hand.
//
// The lock now lives in a private namespace of the APPLYING effective user:
//
//   - root: /var/run/gonf-crontab. /var/run is root-owned mode 0755 on Linux
//     (a /run symlink), FreeBSD, OpenBSD and NetBSD and is emptied at boot.
//     Gonf never creates it: a missing /var/run is reported, not invented.
//   - any other account: <passwd home>/.cache/gonf-crontab. The home comes
//     from the passwd database, not $HOME, so every session of the account
//     agrees on one path. A missing ~/.cache is created with mode 0700 (the
//     XDG default) and kept.
//
// The directory holding gonf-crontab must belong to the applying account
// (root for /var/run; a root-owned ~/.cache is refused because a non-root
// apply could never create its lock there). It must not be world-writable,
// and it may be group-writable only through the account's user-private group
// (gid == egid == euid != 0, gonf's shared rule in internal/dirperm), since
// Go and umask-002 systems create ~/.cache as 0775 that way. That is what
// makes the lock directory impossible for another account to preseed or
// swap: only the applying account (and root) can create, rename or replace
// entries in it. lock_setup.go creates and verifies every object through
// descriptors (see there).
//
// Separate namespaces per applying account are a deliberate choice: root
// updating alice's crontab and alice updating her own take DIFFERENT locks.
// Root could lock inside alice's namespace with descriptor-relative no-follow
// opens and fchown, but then alice could block root's management of her
// crontab by holding or damaging that lock. The cost is a race between a
// concurrent root apply and self apply of the same crontab: the worst case
// is a lost update, which can reintroduce a line that the concurrent root
// apply removed (an Absent job or an adopted legacy entry) until the next
// apply converges again. Current conf usage makes that acceptable: root
// manages only the _gogios crontab besides its own, and _gogios never runs
// Gonf itself. Every apply by one account, including all root applies, still
// serialises.
//
// A ~/.cache lock FILE name carries the short host name, so hosts sharing
// one NFS-mounted home do not contend on each other's locks. The host name
// is read once per process and cached, because a run may rename the host
// (frontends_myname does, as root). Remaining edge: a non-root self apply
// started before a rename and one started after it use different lock
// files until both finish. Root's /var/run lock is host-local by nature and
// has no host part, so a rename never splits root's applies. A filesystem
// that cannot flock (EOPNOTSUPP/ENOTSUP) or has run out of locks (ENOLCK,
// e.g. an NFS server without lockd, or a transient lock-table shortage)
// fails the apply with an explanation instead of running unserialised.

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

const (
	crontabLockTimeout = 5 * time.Second
	crontabLockRetry   = 10 * time.Millisecond

	// privilegedCrontabLockDir is root's lock namespace (see the file comment).
	privilegedCrontabLockDir = "/var/run/gonf-crontab"
	// userCrontabLockSubdir is an unprivileged account's lock namespace,
	// relative to its passwd home directory.
	userCrontabLockSubdir = ".cache/gonf-crontab"
)

// Seams swapped by tests only: crontabLockDirOverride replaces the lock
// directory (so tests never touch /var/run or the real home), lockHostName
// supplies the (per-process cached) host part of the lock file name,
// osHostname is shortHostName's source, and flock is flock(2).
var (
	crontabLockDirOverride string
	lockHostName           = cacheHostName(shortHostName)
	osHostname             = os.Hostname
	flock                  = unix.Flock
)

// lockLocation is where one acquisition takes its lock: dir is the lock
// directory; createParent says whether its parent may be created (one level
// only) when missing (/var/run is never created, ~/.cache may be); and
// hostScoped puts the host name into the lock file name (for homes that may
// be NFS-shared, not for /var/run).
type lockLocation struct {
	dir          string
	createParent bool
	hostScoped   bool
}

// lockCrontab acquires the advisory lock for userName's crontab. Acquisition
// is bounded: a stuck Gonf process reports contention instead of waiting
// forever. The returned function releases the lock.
func lockCrontab(userName string) (func() error, error) {
	return lockCrontabWithin(userName, crontabLockTimeout)
}

// inProcessCrontabLocks backs lockCrontabInProcess: one mutex per crontab
// account name, created on first use.
var inProcessCrontabLocks sync.Map

// lockCrontabInProcess serialises crontab transactions within this process
// only. SetRunnersForTest installs it while crontab itself is faked, so
// tests keep the transaction's mutual exclusion without filesystem state.
func lockCrontabInProcess(userName string) (func() error, error) {
	value, _ := inProcessCrontabLocks.LoadOrStore(userName, &sync.Mutex{})
	mu := value.(*sync.Mutex)
	mu.Lock()
	return func() error {
		mu.Unlock()
		return nil
	}, nil
}

func lockCrontabWithin(userName string, timeout time.Duration) (func() error, error) {
	targetUID, err := crontabUserID(userName)
	if err != nil {
		return nil, err
	}
	euid := uint32(unix.Geteuid())
	if err := requireCrontabAccess(userName, targetUID, euid); err != nil {
		return nil, err
	}
	loc, err := crontabLockLocation(euid)
	if err != nil {
		return nil, err
	}
	return lockCrontabIn(loc, euid, userName, timeout)
}

// requireCrontabAccess rejects an apply that can never succeed: crontab -u
// is root-only, so a non-root process may only change its own crontab.
// Failing here, before any lock object exists, keeps such a run from leaving
// state behind.
func requireCrontabAccess(userName string, targetUID, euid uint32) error {
	if euid == 0 || euid == targetUID {
		return nil
	}
	return fmt.Errorf("crontab for %s (uid %d): only root or that account can change it, but Gonf runs as uid %d", userName, targetUID, euid)
}

// crontabLockLocation returns the applying account's private lock location.
func crontabLockLocation(euid uint32) (lockLocation, error) {
	if crontabLockDirOverride != "" {
		return lockLocation{dir: crontabLockDirOverride, createParent: true, hostScoped: true}, nil
	}
	if euid == 0 {
		return lockLocation{dir: privilegedCrontabLockDir}, nil
	}
	account, err := user.LookupId(strconv.FormatUint(uint64(euid), 10))
	if err != nil {
		return lockLocation{}, fmt.Errorf("look up home directory of uid %d for the crontab lock: %w", euid, err)
	}
	if !filepath.IsAbs(account.HomeDir) {
		return lockLocation{}, fmt.Errorf("uid %d has no absolute home directory (%q) for the crontab lock", euid, account.HomeDir)
	}
	return lockLocation{dir: filepath.Join(account.HomeDir, userCrontabLockSubdir), createParent: true, hostScoped: true}, nil
}

// lockCrontabIn is the testable core: it takes userName's lock inside
// loc.dir, which must be (or become) a 0700 directory owned by ownerUID.
// Objects created by a failed attempt are removed again (see lockSetup).
func lockCrontabIn(loc lockLocation, ownerUID uint32, userName string, timeout time.Duration) (func() error, error) {
	name, err := crontabLockFileName(userName, loc.hostScoped)
	if err != nil {
		return nil, err
	}
	setup := &lockSetup{ownerUID: ownerUID}
	defer setup.closeAll()
	fd, err := setup.acquire(loc, name, timeout)
	if err != nil {
		setup.rollback()
		return nil, fmt.Errorf("lock crontab for %s: %w", userName, err)
	}
	return crontabUnlocker(fd, userName), nil
}

func crontabUnlocker(fd int, userName string) func() error {
	return func() error {
		unlockErr := flock(fd, unix.LOCK_UN)
		closeErr := unix.Close(fd)
		if unlockErr != nil {
			return fmt.Errorf("unlock crontab for %s: %w", userName, unlockErr)
		}
		if closeErr != nil {
			return fmt.Errorf("close crontab lock: %w", closeErr)
		}
		return nil
	}
}

// crontabLockFileName names the lock after the crontab account NAME (the
// spool keeps one crontab per name, even for names sharing a UID, such as
// root/toor) and, when hostScoped, after the host, which keeps hosts sharing
// an NFS home apart. Hashing keeps arbitrary account names out of the path.
func crontabLockFileName(userName string, hostScoped bool) (string, error) {
	digest := sha256.Sum256([]byte(userName))
	if !hostScoped {
		return fmt.Sprintf("%x.lock", digest[:]), nil
	}
	host, err := lockHostName()
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s.%x.lock", host, digest[:]), nil
}

// cacheHostName returns lookup memoised for the life of the process, so a
// host rename during a run cannot move this process's ~/.cache lock file.
func cacheHostName(lookup func() (string, error)) func() (string, error) {
	return sync.OnceValues(lookup)
}

// shortHostName returns the host name up to its first dot, restricted to
// characters that are safe in a file name.
func shortHostName() (string, error) {
	name, err := osHostname()
	if err != nil {
		return "", fmt.Errorf("host name for the crontab lock: %w", err)
	}
	short, _, _ := strings.Cut(name, ".")
	safe := strings.Map(func(r rune) rune {
		if r == '-' || r == '_' || (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			return r
		}
		return '_'
	}, short)
	if safe == "" {
		return "", fmt.Errorf("host name %q is empty for the crontab lock", name)
	}
	return safe, nil
}

func crontabUserID(userName string) (uint32, error) {
	u, err := user.Lookup(userName)
	if err != nil {
		return 0, fmt.Errorf("lookup crontab user %q: %w", userName, err)
	}
	uid, err := strconv.ParseUint(u.Uid, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("parse uid %q for crontab user %q: %w", u.Uid, userName, err)
	}
	return uint32(uid), nil
}

// errLockUnsupported marks a lock file whose filesystem does not implement
// flock at all (EOPNOTSUPP/ENOTSUP). Nobody can hold a lock there, so the
// objects this attempt created are safe to remove again. ENOLCK is NOT
// such an error: it can be transient (lock table exhaustion, a lost NFSv4
// lock) while another process holds the lock, so it never triggers rollback.
var errLockUnsupported = errors.New("the filesystem does not support file locking")

// flockWithin takes an exclusive flock on fd, polling until timeout. path
// is only used in messages.
func flockWithin(fd int, path string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		err := flock(fd, unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return nil
		}
		if lockUnsupported(err) {
			return fmt.Errorf("flock %s: %w (%w); Gonf will not update the crontab unserialised, so move the home directory's .cache to a local filesystem or enable locking on it", path, errLockUnsupported, err)
		}
		if errors.Is(err, unix.ENOLCK) {
			return fmt.Errorf("flock %s: no locks available (%w); the system lock table may be exhausted or the filesystem (e.g. NFS without lockd) cannot grant locks; Gonf will not update the crontab unserialised, retry later or enable locking", path, err)
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			return fmt.Errorf("flock %s: %w", path, err)
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("timed out after %s waiting for %s", timeout, path)
		}
		time.Sleep(crontabLockRetry)
	}
}

// lockUnsupported reports flock errors that mean "this filesystem does not
// implement locking" (permanently, so nobody holds the lock). EOPNOTSUPP and
// ENOTSUP differ on OpenBSD. ENOLCK is deliberately excluded (see
// errLockUnsupported).
func lockUnsupported(err error) bool {
	return errors.Is(err, unix.EOPNOTSUPP) || errors.Is(err, unix.ENOTSUP)
}
