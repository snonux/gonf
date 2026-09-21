package api

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/snonux/gonf/internal/logger"
	"github.com/snonux/gonf/plan"
	"golang.org/x/sys/unix"
)

// stageBlobs is RecordPlan's persistent-directory path: it records into a
// private staging directory and copies the blobs the finished plan references
// into planDir only after RecordPlanTo returned a plan.
//
// Why stage instead of validating first: blob refs are packaged while task
// bodies run (packageDraft, one draft at a time), long before the whole plan
// exists to be validated, and a record can also fail late for reasons that have
// nothing to do with dependencies (a cycle, a packaging error, a body error).
// Staging closes that whole class at once: whatever makes the record fail, the
// destination was never touched.
//
// The staging directory is created lazily (lazyStage), on the first blob
// write, so a plan without blobs never touches $TMPDIR. It is removed on every
// return path (refusal, error, success) and, through logger.OnFatal, when a
// task body ends the process with logger.Fatal. It is NOT removed when the
// process is killed (SIGKILL, SIGINT with no handler, a crash): the staging
// directory (owner-only, 0700, like its blobs) then stays in $TMPDIR until the
// operating system cleans it. Blob-less plans, the common case, never create
// one.
func stageBlobs(planID, planDir string, taskNames []string) ([]plan.Op, error) {
	if err := checkPlanDirUsable(planDir); err != nil {
		return nil, fmt.Errorf("RecordPlan: plan dir: %w", err)
	}
	stage := &lazyStage{}
	defer stage.remove()
	defer logger.OnFatal(stage.remove)()

	ops, err := RecordPlanTo(planID, stage, taskNames...)
	if err != nil {
		return nil, err
	}
	if err := commitStagedBlobs(ops, stage.path(), planDir); err != nil {
		return nil, fmt.Errorf("RecordPlan: plan dir: %w", err)
	}
	return ops, nil
}

// lazyStage is the staging blob store of stageBlobs: a plan.BlobStore that
// creates its private temporary directory only when the first blob is
// written, then delegates to a plan.Store rooted there. Recording is
// single-goroutine and the logger's fatal hook runs on the goroutine that
// called Fatal, so the mutex is cheap insurance that the directory name and
// its removal stay consistent, not a concurrency feature.
type lazyStage struct {
	mu    sync.Mutex
	dir   string
	store *plan.Store
}

var _ plan.BlobStore = (*lazyStage)(nil)

// open returns the underlying store, creating the staging directory first when
// this is the first write. os.MkdirTemp makes it 0700.
func (l *lazyStage) open() (*plan.Store, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.store == nil {
		dir, err := os.MkdirTemp("", "gonf-plan-stage-*")
		if err != nil {
			return nil, fmt.Errorf("RecordPlan: staging dir: %w", err)
		}
		l.dir, l.store = dir, plan.NewStore(dir)
	}
	return l.store, nil
}

// path is the staging directory, or "" when no blob was ever written.
func (l *lazyStage) path() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.dir
}

// remove deletes the staging directory if one was created; it is idempotent
// (the deferred call after a successful run finds nothing left to do).
func (l *lazyStage) remove() {
	l.mu.Lock()
	dir := l.dir
	l.dir, l.store = "", nil
	l.mu.Unlock()
	if dir != "" {
		_ = os.RemoveAll(dir)
	}
}

// WriteFile implements plan.BlobStore.
func (l *lazyStage) WriteFile(name string, data []byte) (string, error) {
	s, err := l.open()
	if err != nil {
		return "", err
	}
	return s.WriteFile(name, data)
}

// WriteTree implements plan.BlobStore.
func (l *lazyStage) WriteTree(name, srcDir string) (string, error) {
	s, err := l.open()
	if err != nil {
		return "", err
	}
	return s.WriteTree(name, srcDir)
}

// WriteGlob implements plan.BlobStore.
func (l *lazyStage) WriteGlob(name, pattern string) (string, error) {
	s, err := l.open()
	if err != nil {
		return "", err
	}
	return s.WriteGlob(name, pattern)
}

// checkPlanDirUsable is the cheap up-front counterpart of commitStagedBlobs's
// plan.SecureDir. It runs before any task body so the common unusable
// destinations fail early, without creating or changing anything. It looks at
// the same things SecureDir would trip over:
//
//   - planDir or any of its ancestors is a symlink (SecureDir opens every path
//     component with O_NOFOLLOW; filepath.Clean first, so "link/" is the
//     symlink "link", which a plain Lstat of "link/" would follow);
//   - an existing planDir fails plan.CheckExistingDir, SecureDir's own
//     acceptance rule for a pre-existing directory: it must be a directory,
//     owned by the effective user (root included) and not writable by group or
//     others. SecureDir never chmods such a directory, so a 0755 one of ours
//     passes and stays 0755, while a group-writable, world-writable (sticky
//     /tmp too) or foreign-owned one is refused;
//   - an existing planDir we cannot write to (a read-only directory of ours:
//     SecureDir no longer makes it writable by chmod'ing it). SecureDir itself
//     does not test this, the write that follows would fail; the check turns
//     that into an early refusal;
//   - an absent planDir has no existing ancestor we can create entries in.
//
// It is a best-effort pre-check, not a guarantee: it inspects the path with
// Lstat/access(2) while SecureDir opens it component by component, so the
// answers can differ when the path changes in between or on exotic setups
// (security modules, unusual ACLs). SecureDir at commit time always has the
// last word and refuses safely, writing nothing to a directory it cannot take
// over.
func checkPlanDirUsable(planDir string) error {
	planDir = filepath.Clean(planDir)
	if err := refuseSymlinkedPath(planDir); err != nil {
		return err
	}
	info, err := os.Lstat(planDir)
	switch {
	case err == nil:
		return checkExistingPlanDir(planDir, info)
	case !errors.Is(err, os.ErrNotExist):
		return err
	}
	for parent := filepath.Dir(planDir); ; parent = filepath.Dir(parent) {
		info, err := os.Stat(parent)
		if errors.Is(err, os.ErrNotExist) && parent != filepath.Dir(parent) {
			continue
		}
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return fmt.Errorf("%s is not a directory", parent)
		}
		if err := unix.Access(parent, unix.W_OK|unix.X_OK); err != nil {
			return fmt.Errorf("cannot create %s: %w", planDir, err)
		}
		return nil
	}
}

// refuseSymlinkedPath walks path (already cleaned) and every ancestor up to the
// root, or up to "." for a relative path, without following symlinks, and
// refuses the first one that is a symlink. plan.SecureDir refuses such paths
// too (O_NOFOLLOW on each component), so a symlinked plan directory or ancestor
// is reported as what it is, before any task body runs, instead of as a
// generic "not a directory". Components that do not exist or cannot be
// examined are skipped here; the checks after this one report those.
func refuseSymlinkedPath(path string) error {
	for p := path; ; p = filepath.Dir(p) {
		if info, err := os.Lstat(p); err == nil && info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%s is a symlink; symlinked plan directories are refused", p)
		}
		if filepath.Dir(p) == p {
			return nil
		}
	}
}

// checkExistingPlanDir is the existing-directory half of checkPlanDirUsable:
// path (already Lstat'ed as info, and known not to be a symlink because
// refuseSymlinkedPath ran first) must satisfy plan.SecureDir's acceptance rule
// and be writable and searchable by us.
func checkExistingPlanDir(path string, info os.FileInfo) error {
	if err := plan.CheckExistingDir(path, info); err != nil {
		return err
	}
	if err := unix.Access(path, unix.W_OK|unix.X_OK); err != nil {
		return fmt.Errorf("cannot write to %s: %w", path, err)
	}
	return nil
}

// commitStagedBlobs makes planDir exist (created 0700 when missing, an existing
// one is verified by plan.SecureDir and never chmod'ed) and copies every blob the
// recorded ops reference from the staging directory into it. Blobs of earlier
// plans that this plan does not reference stay untouched; a blob with the same
// ref is replaced (the store's own write semantics: files atomically, trees
// cleared and recreated). It is the only step that writes planDir, and it runs
// after every validation, so what can still fail here is I/O on the destination
// itself (permissions, full disk); those errors may leave some blobs copied,
// which is unavoidable without transactional directories.
func commitStagedBlobs(ops []plan.Op, stage, planDir string) error {
	if err := plan.SecureDir(planDir); err != nil {
		return err
	}
	dest := plan.NewStore(planDir)
	copied := make(map[string]bool)
	for _, op := range ops {
		if op.Blob == "" || copied[op.Blob] {
			continue
		}
		copied[op.Blob] = true
		if err := copyStagedBlob(stage, dest, op.Blob); err != nil {
			return err
		}
	}
	return nil
}

// copyStagedBlob copies one staged blob (a single file, or a tree/glob
// directory) into dest under the same ref. Trees go back through WriteTree,
// which packages through the same neutral manifest that produced the staged
// copy, so the result is identical to what a direct write would have made.
func copyStagedBlob(stage string, dest *plan.Store, ref string) error {
	src, err := plan.Resolve(stage, ref)
	if err != nil {
		return err
	}
	info, err := os.Lstat(src)
	if err != nil {
		return fmt.Errorf("staged blob %q: %w", ref, err)
	}
	name := strings.TrimPrefix(ref, "blobs/")
	var got string
	if info.IsDir() {
		got, err = dest.WriteTree(name, src)
	} else {
		var data []byte
		if data, err = os.ReadFile(src); err != nil {
			return fmt.Errorf("staged blob %q: %w", ref, err)
		}
		got, err = dest.WriteFile(name, data)
	}
	if err != nil {
		return err
	}
	if got != ref {
		return fmt.Errorf("staged blob %q was written as %q", ref, got)
	}
	return nil
}
