package api

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

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
// return path (refusal, error, success); DSL misuse in a task body is a
// returned record error (internal/declerr), never a process exit, so the
// deferred removal always runs. It is NOT removed when the process is killed (SIGKILL, SIGINT with no handler, a crash): the staging
// directory (owner-only, 0700, like its blobs) then stays in $TMPDIR until the
// operating system cleans it. Blob-less plans, the common case, never create
// one.
func stageBlobs(planID, planDir string, taskNames []string) ([]plan.Op, error) {
	if err := checkPlanDirUsable(planDir); err != nil {
		return nil, fmt.Errorf("RecordPlan: plan dir: %w", err)
	}
	stage := &lazyStage{}
	defer stage.remove()

	ops, err := RecordPlanTo(planID, stage, taskNames...)
	if err != nil {
		return nil, err
	}
	if err := commitStagedBlobs(ops, stage.path(), planDir); err != nil {
		return nil, err
	}
	return ops, nil
}

// lazyStage is the staging blob store of stageBlobs: a plan.BlobStore that
// creates its private temporary directory only when the first blob is
// written, then delegates to a plan.Store rooted there. Recording is
// single-goroutine, so the mutex is cheap insurance that the directory name
// and its removal stay consistent, not a concurrency feature.
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
// plan.OpenSecureStore (SecureDir's walk). It runs before any task body so
// the common unusable destinations fail early, without creating or changing
// anything. It looks at the same things SecureDir would trip over:
//
//   - planDir or any of its ancestors is a symlink (SecureDir opens every path
//     component with O_NOFOLLOW; filepath.Clean first, so "link/" is the
//     symlink "link", which a plain Lstat of "link/" would follow);
//   - an existing planDir fails plan.CheckExistingDir, SecureDir's own
//     acceptance rule for a pre-existing directory: it must be a directory,
//     owned by the effective user (root included), not world-writable and not
//     writable by a group other than the caller's private group (see
//     plan.checkDirAttrs; a 0775 checkout of a user-private-group user passes).
//     SecureDir never chmods such a directory, so a 0755 one of ours passes and
//     stays 0755, while a world-writable (sticky /tmp too), shared-group-writable
//     or foreign-owned one is refused;
//   - an existing planDir we cannot write to (a read-only directory of ours,
//     0555/0500: SecureDir no longer makes it writable by chmod'ing it, which
//     is new since m62, before it the directory was chmod'ed to 0700 and the
//     run worked). SecureDir itself does not test this, the write that follows
//     would fail; the check turns that into an early refusal that says how to
//     fix it;
//   - an absent planDir has no existing ancestor we can create entries in.
//
// An existing <planDir>/blobs is deliberately NOT inspected here: whether the
// plan needs blobs/ at all is only known after the task bodies ran, and a
// blob-less plan never touches it, so refusing an unsafe leftover blobs/
// up front would reject plans that would have worked. An unsafe blobs/ (a
// symlink, foreign-owned, world- or shared-group-writable) is therefore
// refused at commit time, by the same directory rule applied to blobs/ (see
// plan.Store: blobs/ is opened without following a symlink below the plan
// directory descriptor that plan.OpenSecureStore verified), before any blob
// is written.
//
// It is a best-effort pre-check, not a guarantee: it inspects the path with
// Lstat/access(2) before any task body runs, and the path can change before
// the commit. commitStagedBlobs therefore re-checks at commit time, before the
// first blob is written: plan.OpenSecureStore applies the shared directory
// rule (a real directory, no symlinked component, owned by the caller, no
// unsafe group/world write) and keeps the verified directory's descriptor for
// the writes, and requireWritableStore re-checks the caller's write
// permission, which is not part of that rule, on that same held directory.
// Either refusal writes nothing. Errors during the writes themselves (for
// example a leftover blobs/ or blob tree the caller cannot write or replace,
// a full disk, or the directory made read-only after the re-check) surface as
// plain I/O errors with the non-atomic commit caveat documented on RecordPlan.
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
	return requireWritableDir(path)
}

// requireWritableDir refuses a directory the caller cannot create entries in,
// with an actionable message. It is the up-front check (checkExistingPlanDir)
// and looks at the path; the commit-time re-check (requireWritableStore) asks
// about the directory it holds instead. Both exist because writability is not
// part of the shared directory rule that plan.SecureDir enforces.
func requireWritableDir(path string) error {
	return writableRefusal(path, unix.Access(path, unix.W_OK|unix.X_OK))
}

// requireWritableStore is requireWritableDir for the plan directory
// commitStagedBlobs holds: dest.CheckWritable asks about the verified
// directory through its descriptor, so a path swapped for a symlink (to a
// writable directory) after OpenSecureStore cannot make a read-only held
// directory pass and leave it half-updated. path only names it in the message.
func requireWritableStore(dest *plan.Store, path string) error {
	return writableRefusal(path, dest.CheckWritable())
}

// writableRefusal words a failed write-permission check of path (err nil:
// no refusal).
func writableRefusal(path string, err error) error {
	if err == nil {
		return nil
	}
	// plan.DirLabel, like every other refusal of the shared rule, so the
	// default "-o ." reads as the working directory, not as "cannot write to .".
	return fmt.Errorf("cannot write to %s: %w; run chmod u+w on it, "+
		"or choose a writable private directory you own (-o <private dir>)", plan.DirLabel(path), err)
}

// commitStagedBlobs makes planDir exist (created 0700 when missing, an existing
// one is verified by SecureDir's rule and never chmod'ed) and copies every blob
// the recorded ops reference from the staging directory into it. Blobs of
// earlier plans that this plan does not reference stay untouched; a blob with
// the same ref is replaced (the store's own write semantics: files atomically,
// trees cleared and recreated). It is the only step that writes blobs into
// planDir. It runs after the plan validations, but refuses too:
// plan.OpenSecureStore and requireWritableStore re-check planDir (the latter
// through the held descriptor) before the first blob is written (a refusal
// writes nothing), and every blob write checks
// blobs/ against the directory rule. All writes go through the descriptor of
// the planDir that OpenSecureStore verified and the blobs/ descriptor opened
// below it without following symlinks, never through the path, so swapping
// planDir or blobs/ after the checks cannot redirect a blob.
// Beyond those refusals, what can fail here is I/O on the destination itself
// (permissions of leftovers, full disk); such errors may leave some blobs
// copied, which is unavoidable without transactional directories.
//
// Error prefixes: a refused planDir is reported as "RecordPlan: plan dir: ...".
// A blob write error (for example an unsafe existing blobs/ directory) is a
// plan.Refusal that names the blob; it is reported as "RecordPlan: <reason>"
// with the store's own "plan: " prefix dropped (recordCommitError), so the
// message has one package prefix, not a "plan dir: ... plan: ..." chain.
func commitStagedBlobs(ops []plan.Op, stage, planDir string) error {
	// OpenSecureStore runs SecureDir's no-follow walk and keeps the descriptor
	// of the directory it verified: every blob is written relative to it, so a
	// planDir swapped for a symlink after this check cannot redirect them.
	dest, err := plan.OpenSecureStore(planDir)
	if err != nil {
		return fmt.Errorf("RecordPlan: plan dir: %w", err)
	}
	defer func() { _ = dest.Close() }()
	planDirVerified(planDir, dest)
	// SecureDir's shared rule does not cover the caller's write permission, so
	// a directory made read-only after the up-front pre-check would pass it and
	// an existing writable blobs/ inside it would be updated before plan.jsonl
	// fails. Re-check writability here, before the first blob is written, of
	// the directory dest holds (not of whatever the path names by now).
	if err := requireWritableStore(dest, planDir); err != nil {
		return fmt.Errorf("RecordPlan: plan dir: %w", err)
	}
	copied := make(map[string]bool)
	for _, op := range ops {
		if op.Blob == "" || copied[op.Blob] {
			continue
		}
		copied[op.Blob] = true
		if err := copyStagedBlob(stage, dest, op.Blob); err != nil {
			return recordCommitError(err)
		}
	}
	return nil
}

// planDirVerified is a test seam, called after OpenSecureStore verified
// planDir and before the writability re-check and the first blob write, with
// the held store, so a test can swap planDir (or its blobs/) for a symlink at
// exactly that point (TestCommitStagedBlobsIgnoresPlanDirSwappedAfterCheck)
// and check that the store is closed afterwards.
var planDirVerified = func(planDir string, dest *plan.Store) {}

// recordCommitError words a failure to copy a staged blob as "RecordPlan: ...".
// The blob store's write errors are plan.Refusals whose Reason is the message
// without the store's "plan: " prefix; showing that avoids "RecordPlan: plan:
// ..." (and, at `gonf plan`, "plan: RecordPlan: plan: ..."). The original error
// stays reachable through errors.Is/As. Any other error keeps its own text.
func recordCommitError(err error) error {
	var refusal plan.Refusal
	if errors.As(err, &refusal) {
		return &refusedError{msg: "RecordPlan: " + refusal.Reason(), cause: err}
	}
	return fmt.Errorf("RecordPlan: %w", err)
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
