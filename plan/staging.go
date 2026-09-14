package plan

import (
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/snonux/gonf/internal/logger"
)

// applyRunTTL bounds how long a staging run directory may live before the
// sweep removes it. It is deliberately generous: a legitimate "gonf apply"
// never runs anywhere near this long, so only leftovers from killed or
// crashed applies ever reach the age. It is a variable so tests can shrink
// it (or negate it).
var applyRunTTL = 24 * time.Hour

// runDirPrefix matches the run directories created by NewApplyRunDir.
const runDirPrefix = "run-"

// ApplyStagingRoot returns $TMPDIR/gonf-apply/<uid> (owner-only).
func ApplyStagingRoot() (string, error) {
	uid := "nouser"
	if u, err := user.Current(); err == nil && u.Uid != "" {
		uid = u.Uid
	} else {
		uid = strconv.Itoa(os.Getuid())
	}
	root := filepath.Join(os.TempDir(), "gonf-apply", uid)
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", fmt.Errorf("plan apply staging: %w", err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		return "", fmt.Errorf("plan apply staging chmod: %w", err)
	}
	return root, nil
}

// SweepApplyStaging removes run directories under root whose modification
// time is older than applyRunTTL (leftovers from killed or crashed applies).
// Fresh directories are never touched, so the sweep is safe to run while
// other applies are in flight. Entries that are not run directories are left
// alone. Per-entry failures do not stop the sweep; the joined error is
// best-effort and callers must treat it as such (see NewApplyRunDir).
func SweepApplyStaging(root string) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("plan apply sweep %s: %w", root, err)
	}
	now := time.Now()
	var errs []error
	for _, e := range entries {
		if !staleRunDir(e, now) {
			continue
		}
		path := filepath.Join(root, e.Name())
		if err := os.RemoveAll(path); err != nil {
			errs = append(errs, fmt.Errorf("plan apply sweep %s: %w", path, err))
		}
	}
	return errors.Join(errs...)
}

// staleRunDir reports whether the entry is a run directory old enough to be
// swept. Anything that is not a run directory (other names, plain files,
// symlinks) is left alone, as are entries whose age cannot be determined.
func staleRunDir(e os.DirEntry, now time.Time) bool {
	if !e.IsDir() || !strings.HasPrefix(e.Name(), runDirPrefix) {
		return false
	}
	info, err := e.Info()
	if err != nil {
		// Vanished mid-sweep or unreadable: leave it to the next sweep.
		return false
	}
	return now.Sub(info.ModTime()) > applyRunTTL
}

// NewApplyRunDir creates a fresh 0700 run directory for one apply, after a
// best-effort sweep of stale leftovers. Concurrent applies each get their own
// directory and remove it again via the returned cleanup, so they never
// interfere with each other. A sweep failure is logged at debug level and
// ignored: the garbage collection must never break a new apply.
func NewApplyRunDir() (dir string, cleanup func(), err error) {
	root, err := ApplyStagingRoot()
	if err != nil {
		return "", nil, err
	}
	if err := SweepApplyStaging(root); err != nil {
		logger.Debug("plan apply staging sweep (ignored): %v", err)
	}
	dir, err = os.MkdirTemp(root, runDirPrefix+"*")
	if err != nil {
		return "", nil, err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		_ = os.RemoveAll(dir)
		return "", nil, err
	}
	cleanup = func() { _ = os.RemoveAll(dir) }
	return dir, cleanup, nil
}
