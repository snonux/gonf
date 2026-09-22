package configset

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/snonux/gonf/internal/logger"
	"github.com/snonux/gonf/internal/safepath"
	"golang.org/x/sys/unix"
)

// Pending markers carry a member's "published but not yet signalled" state
// across a crash. Each member has its own marker, a small file in the
// member's own directory:
//
//	.gonf-pending.<24 hex digits of sha256(set, key, path)>
//
// It is created (O_EXCL, O_NOFOLLOW, 0600, fsync of file and directory)
// right before that member's live rename, while the apply holds the member
// directory locks, and removed (unlink, directory fsync) only after the apply
// has reported the member as changed. Any later apply of a set with the same
// name and a member with the same key at the same path finds the marker and
// reports that member as changed even when its live content already matches,
// so its OnChange gates fire.
//
// Because the name hashes set, key and path, two recipes that both define a
// set called "nsd" never consume each other's markers unless they manage the
// very same member, and there is nothing to configure: no state directory,
// no dependency on $HOME or $XDG_STATE_HOME, no extra lock. A marker whose
// member later moves or is removed from the set is orphaned; that is
// harmless (the member at its new path is published and signalled anyway)
// and an operator can delete it.
//
// What this guarantees: a signal survives a crash (or a failed apply) between
// a member's rename and the end of its set's apply. What it does not: the
// marker is removed when the set reports the change, before any gated Command
// or Service runs, so a crash after that point, or a failing gated action,
// loses the signal; change reports are process-local. Marker handling errs on
// the side of over-signalling: a marker that cannot be unlinked after
// reporting (a warning) or during a rollback stays, so the next apply signals
// its member once more; one that was unlinked but whose directory fsync
// failed may come back after a power loss and then signal once more. A failed
// creation normally unlinks the marker again, so a member that was never
// published is not signalled; only if that unlink fails too (reported in the
// error) can a later apply signal it. Pruning directory syncs over a member
// directory delete markers (see docs/config-set.md).

// markerPrefix starts every marker name.
const markerPrefix = ".gonf-pending."

// markerBody is written into a marker for operators; applies only need the
// marker to exist.
type markerBody struct {
	Set    string `json:"set"`
	Member string `json:"member"`
	Path   string `json:"path"`
}

// markerName is the marker file name of member m of set name.
func markerName(name string, m memberSpec) string {
	sum := sha256.Sum256([]byte(name + "\x00" + m.key + "\x00" + m.path))
	return markerPrefix + hex.EncodeToString(sum[:12])
}

// hasMarker reports whether member m of set name has a pending marker. The
// marker is opened without following a symlink and without blocking, and must
// be a regular file owned by the applying uid (sys.euid); anything else is
// refused rather than trusted or ignored. A missing member directory means no
// marker.
func (sys *system) hasMarker(name string, m memberSpec) (bool, error) {
	dir := filepath.Dir(m.path)
	dfd, err := openDir(dir, safepath.Walk{})
	if errors.Is(err, unix.ENOENT) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer func() { _ = unix.Close(dfd) }()
	marker := markerName(name, m)
	f, err := safepath.OpenRegularAt(dfd, marker, filepath.Join(dir, marker))
	if errors.Is(err, unix.ENOENT) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("pending marker %s for member %s: %w", filepath.Join(dir, marker), m.key, err)
	}
	defer func() { _ = f.Close() }()
	info, err := safepath.Fstat(int(f.Fd()))
	if err != nil {
		return false, err
	}
	if info.UID != uint32(sys.euid()) {
		return false, fmt.Errorf("pending marker %s is not owned by the applying user", filepath.Join(dir, marker))
	}
	return true, nil
}

// createMarker creates member m's marker unless one exists already (from an
// earlier, unsignalled publication), and makes it durable: file fsync, then
// directory fsync (sys.syncDirFD). Called right before the member's live
// rename.
func (sys *system) createMarker(name string, m memberSpec) (err error) {
	dir := filepath.Dir(m.path)
	dfd, err := openDir(dir, safepath.Walk{})
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(dfd) }()
	marker := markerName(name, m)
	fd, err := unix.Openat(dfd, marker, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if errors.Is(err, unix.EEXIST) {
		// Pending already; hasMarker verified it (or will refuse it).
		return nil
	}
	if err != nil {
		return fmt.Errorf("create pending marker %s: %w", filepath.Join(dir, marker), err)
	}
	// From here on the marker exists, but its member is not published yet:
	// any failure must unlink it again, or a later apply would signal a
	// member this apply never renamed.
	err = writeMarkerBody(fd, markerBody{Set: name, Member: m.key, Path: m.path})
	if err == nil {
		err = sys.syncDirFD(dfd, dir)
	}
	if err != nil {
		if uerr := sys.unlinkAt(dfd, marker, 0); uerr != nil {
			err = errors.Join(err, fmt.Errorf("unlink the unused marker (a later apply may signal member %s once): %w", m.key, uerr))
		}
		return fmt.Errorf("create pending marker %s: %w", filepath.Join(dir, marker), err)
	}
	return nil
}

// writeMarkerBody writes body into the new marker fd, fsyncs and closes it.
// Only the marker's existence carries meaning (the directory fsync makes that
// durable); the file fsync merely keeps the operator-facing content intact
// after a crash.
func writeMarkerBody(fd int, body markerBody) error {
	f := os.NewFile(uintptr(fd), "marker")
	data, err := json.Marshal(body)
	if err == nil {
		_, err = f.Write(append(data, '\n'))
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// markerRemovalError is a failed marker removal. unlinked tells the two
// consequences apart: when the unlink itself failed the marker stays and the
// next apply signals the member once more; when only the directory fsync
// after the unlink failed the marker is gone but may come back after a power
// loss, so a later apply may signal once more.
type markerRemovalError struct {
	key      string
	unlinked bool
	err      error
}

func (e *markerRemovalError) Error() string {
	if e.unlinked {
		return fmt.Sprintf("%v; the marker is removed but not durably, so after a power loss a later apply may signal member %s once more", e.err, e.key)
	}
	return fmt.Sprintf("%v; the marker stays, so the next apply signals member %s once more", e.err, e.key)
}

func (e *markerRemovalError) Unwrap() error { return e.err }

// removeMarker deletes member m's marker, if any (sys.unlinkAt), and fsyncs
// the directory (sys.syncDirFD, by default with the policy of fsyncDir) so
// the removal is durable once it returns. A failure is a *markerRemovalError.
func (sys *system) removeMarker(name string, m memberSpec) error {
	dir := filepath.Dir(m.path)
	dfd, err := openDir(dir, safepath.Walk{})
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return &markerRemovalError{key: m.key, err: err}
	}
	defer func() { _ = unix.Close(dfd) }()
	marker := markerName(name, m)
	err = sys.unlinkAt(dfd, marker, 0)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return &markerRemovalError{key: m.key, err: fmt.Errorf("remove pending marker %s: %w", filepath.Join(dir, marker), err)}
	}
	if err := sys.syncDirFD(dfd, dir); err != nil {
		return &markerRemovalError{key: m.key, unlinked: true, err: err}
	}
	return nil
}

// pendingMembers returns the keys of the members that have a marker.
func (s *spec) pendingMembers() (map[string]bool, error) {
	pending := map[string]bool{}
	for _, m := range s.members {
		ok, err := s.sys.hasMarker(s.name, m)
		if err != nil {
			return nil, fmt.Errorf("config set %s: %w", s.name, err)
		}
		if ok {
			pending[m.key] = true
		}
	}
	return pending, nil
}

// removeMarkers removes the markers of the given members after they were
// reported. The change is already recorded by then, so a failure is only a
// warning (worded by markerRemovalError: the member is, or may be, signalled
// once more), which is safe, whereas failing here would not un-report
// anything.
func (s *spec) removeMarkers(keys map[string]bool) {
	for _, m := range s.members {
		if !keys[m.key] {
			continue
		}
		if err := s.sys.removeMarker(s.name, m); err != nil {
			logger.Warn("config set %s: %v", s.name, err)
		}
	}
}
