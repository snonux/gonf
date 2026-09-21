package plan

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"golang.org/x/sys/unix"
)

// Store writes blob sidecars under Root/blobs/. It has two modes, which
// differ only in how Root itself is reached; blobs/ below it is treated the
// same way in both (see openBlobsDir):
//
//   - NewStore (path mode) reaches Root by path, following symlinks, and
//     creates it when missing. It is for a private directory the caller made
//     itself below $TMPDIR (RecordPlan's staging store, Run's and Apply's temp
//     plan directories), whose path may legitimately pass through a symlink
//     (macOS: /var -> /private/var).
//   - OpenSecureStore (held mode) verifies Root once with SecureDir's
//     no-follow walk of its whole path and keeps the descriptor of the
//     directory it verified; every write is then made relative to that
//     descriptor, never through the path again. It is for the plan directory
//     the operator names (`gonf plan -o dir`), so a Root swapped for a symlink
//     after the check cannot redirect blobs (which may carry secret material).
//     Close releases the descriptor.
type Store struct {
	Root string
	held *heldRoot // nil in path mode
}

// heldRoot is the verified plan directory of a held-mode Store.
type heldRoot struct {
	fd     int
	closed bool
}

// NewStore returns a path-mode blob store rooted at planDir (the directory
// that will hold plan.jsonl alongside blobs/). planDir is reached following
// symlinks and is not verified; only blobs/ is. Use OpenSecureStore for a
// directory the operator names.
func NewStore(planDir string) *Store {
	return &Store{Root: planDir}
}

// OpenSecureStore makes sure planDir exists and passes SecureDir's rule
// (created 0700 when missing, an existing one verified and never chmod'ed, no
// symlink anywhere in its path) and returns a held-mode store that writes
// every blob relative to the descriptor of exactly that directory. The caller
// must Close it. Its errors are SecureDir's, without a package prefix.
func OpenSecureStore(planDir string) (*Store, error) {
	fd, err := openSecureDir(planDir)
	if err != nil {
		return nil, err
	}
	return &Store{Root: planDir, held: &heldRoot{fd: fd}}, nil
}

// Close releases the plan directory descriptor of a held-mode store; later
// writes are refused. It is a no-op for a path-mode store and idempotent.
func (s *Store) Close() error {
	if s == nil || s.held == nil || s.held.closed {
		return nil
	}
	s.held.closed = true
	return unix.Close(s.held.fd)
}

// CheckWritable reports, as access(2) with W_OK|X_OK would, whether the
// caller may create entries in the store's plan directory; the error is the
// raw cause (EACCES, ...) for the caller to word. A held-mode store asks about
// the directory it verified, through its descriptor (faccessat on "."), never
// about whatever the path names by now; a path-mode store checks the path. A
// closed held-mode store is refused.
func (s *Store) CheckWritable() error {
	if err := s.usable(); err != nil {
		return err
	}
	if s.held != nil {
		return unix.Faccessat(s.held.fd, ".", unix.W_OK|unix.X_OK, 0)
	}
	return unix.Access(s.Root, unix.W_OK|unix.X_OK)
}

// Resolve returns the absolute path for blobRef (e.g. "blobs/systemd-user").
// It errors when the id is missing, invalid, or not present on disk.
func Resolve(planDir, blobRef string) (string, error) {
	if blobRef == "" {
		return "", fmt.Errorf("plan: missing blob id")
	}
	if err := validateBlobRef(blobRef); err != nil {
		return "", err
	}
	if planDir == "" {
		return "", fmt.Errorf("plan: blob %q: no plan directory", blobRef)
	}
	abs := filepath.Join(planDir, filepath.FromSlash(blobRef))
	if _, err := os.Lstat(abs); err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("plan: missing blob %q", blobRef)
		}
		return "", fmt.Errorf("plan: blob %q: %w", blobRef, err)
	}
	return abs, nil
}

// ReadFile reads a blob that is a single regular file.
func ReadFile(planDir, blobRef string) ([]byte, error) {
	abs, err := Resolve(planDir, blobRef)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(abs)
	if err != nil {
		return nil, fmt.Errorf("plan: blob %q: %w", blobRef, err)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("plan: blob %q is a directory, not a file", blobRef)
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return nil, fmt.Errorf("plan: read blob %q: %w", blobRef, err)
	}
	return data, nil
}

// WriteFile writes data as blobs/<name> (a single file, 0600, atomic rename)
// and returns the ref. The file is written through the descriptor of the
// blobs/ directory that openBlobsDir verified, so swapping blobs/ (or, in held
// mode, the plan directory) after the check cannot redirect the write.
func (s *Store) WriteFile(name string, data []byte) (string, error) {
	if err := s.usable(); err != nil {
		return "", err
	}
	ref, err := BlobRefFor(name)
	if err != nil {
		return "", err
	}
	blobsFD, err := s.openBlobsDir()
	if err != nil {
		return "", blobErrorf("write blob %q: open private directory: %w", ref, err)
	}
	defer func() { _ = unix.Close(blobsFD) }()
	blobsDirVerified(s.blobsPath())
	if err := writePrivateFileAt(blobsFD, blobLeaf(ref), data); err != nil {
		return "", blobErrorf("write blob %q: %w", ref, err)
	}
	return ref, nil
}

// WriteTree copies srcDir into blobs/<name>/ and returns the ref. The
// tree is packaged through scanTree: directories (empty ones included)
// and symlinks (raw target, dangling included — never read through) are
// preserved as themselves, regular files by content; other file types
// fail loudly. This is the same manifest MemoryStore packages, so a tree
// applied locally matches the same tree pushed to a remote host.
func (s *Store) WriteTree(name, srcDir string) (string, error) {
	if err := s.usable(); err != nil {
		return "", err
	}
	entries, err := scanTree(srcDir)
	if err != nil {
		return "", err
	}
	return s.writeEntries(name, "tree", entries)
}

// WriteGlob copies basename matches of pattern into blobs/<name>/ (flat).
// Glob blobs are flat regular-file pickers classified by GlobMatchCounts:
// symlinks to regular files are read through into content; directories,
// dangling links and other non-regular entries are skipped — the same
// policy the direct WithSourceGlob path applies, so glob-sourced sync_dir
// ops stay identical across local and remote apply. (Tree packaging via
// WriteTree preserves symlinks instead.)
func (s *Store) WriteGlob(name, pattern string) (string, error) {
	if err := s.usable(); err != nil {
		return "", err
	}
	entries, err := scanGlob(pattern)
	if err != nil {
		return "", err
	}
	return s.writeEntries(name, "glob", entries)
}

// writeEntries replaces the tree blob blobs/<name>/ with entries (what names
// the blob kind in errors) and returns its ref. Like WriteFile it works
// relative to the verified blobs/ descriptor from start to end: the old tree
// is removed, the new one created exactly 0700 and filled, all without
// following a symlink (blob_at.go), so swapping blobs/ or anything below it
// after the check cannot redirect the tree. A tree that reappears between the
// removal and the creation is refused, not filled (createTreeRootAt).
func (s *Store) writeEntries(name, what string, entries []BlobEntry) (string, error) {
	ref, err := BlobRefFor(name)
	if err != nil {
		return "", err
	}
	blobsFD, err := s.openBlobsDir()
	if err != nil {
		return "", blobErrorf("blob %q: %w", ref, err)
	}
	defer func() { _ = unix.Close(blobsFD) }()
	blobsDirVerified(s.blobsPath())
	leaf := blobLeaf(ref)
	if err := removeAllAt(blobsFD, leaf, ref); err != nil {
		return "", blobErrorf("clear blob %q: %w", ref, err)
	}
	blobTreeCleared(filepath.Join(s.blobsPath(), leaf))
	treeFD, err := createTreeRootAt(blobsFD, leaf)
	if err != nil {
		return "", blobErrorf("mkdir blob %q: %w", ref, err)
	}
	defer func() { _ = unix.Close(treeFD) }()
	if err := materializeAt(treeFD, entries); err != nil {
		return "", blobErrorf("package %s into %q: %w", what, ref, err)
	}
	return ref, nil
}

// openBlobsDir applies SecureDir's directory policy to <Root>/blobs, and to it
// alone, and returns a descriptor of it that the caller writes through and
// closes. Exactly what is checked:
//
//   - blobs/ itself: missing => created exactly 0700 (whatever the umask);
//     existing => opened without following a symlink (a symlinked blobs/ is
//     refused), and it must be a directory owned by the effective user, not
//     writable by others and not writable by a group other than the caller's
//     private group (checkDirAttrs); it is left unmodified;
//   - Root, by mode. Held mode (OpenSecureStore): nothing more; blobs/ is
//     opened below the descriptor of the plan directory that was verified in
//     full when the store was opened, with no path lookup at all. Path mode
//     (NewStore): Root is created when missing (0700 masked by the umask) and
//     opened following symlinks, and neither it nor its ancestors are
//     verified, because a private $TMPDIR store may be reached through a
//     symlink (see Store).
func (s *Store) openBlobsDir() (int, error) {
	if s.held != nil {
		return openSecureChildAt(s.held.fd, s.Root, "blobs")
	}
	return openSecureChildDir(s.Root, "blobs")
}

// blobsPath is <Root>/blobs, for the blobsDirVerified test seam only.
func (s *Store) blobsPath() string {
	return filepath.Join(s.Root, "blobs")
}

// blobsDirVerified is a test seam, called after openBlobsDir verified blobs/
// and before anything is written below it, so a test can swap blobs/ for a
// symlink at exactly that point (TestStoreWritesStayInVerifiedBlobsDir).
var blobsDirVerified = func(blobsDir string) {}

// blobTreeCleared is a test seam, called by writeEntries after the old tree
// was removed and before the new one is created, so a test can recreate the
// tree at exactly that point (TestWriteTreeRefusesTreeReappearedAfterClear).
var blobTreeCleared = func(tree string) {}

// blobLeaf is the entry name of ref inside blobs/ ("blobs/x" -> "x"). Refs
// come from BlobRefFor, whose sanitized names are a single path component.
func blobLeaf(ref string) string {
	return strings.TrimPrefix(ref, "blobs/")
}

// blobError is an error of the blob store that carries the package prefix: a
// failed or refused write (WriteFile/WriteTree/WriteGlob, directory-policy
// refusals included), the packaging scans they start with (scanTree,
// scanGlob), blob-name/ref validation (BlobRefFor) and a store without a plan
// directory. Its message starts with the package prefix "plan: " like every
// other error of this package, and it implements Refusal, so a caller that adds
// its own prefix (RecordPlan's "RecordPlan: ") can show Reason() and print one
// prefix instead of "RecordPlan: plan: ...". The wrapped cause stays reachable
// with errors.Is/As. Raw I/O errors from walking a source tree carry no "plan:"
// prefix at all and are not blobErrors; they need no re-wording.
type blobError struct{ err error }

func (e *blobError) Error() string  { return "plan: " + e.err.Error() }
func (e *blobError) Reason() string { return e.err.Error() }
func (e *blobError) Unwrap() error  { return e.err }

// usable refuses a store without a plan directory and a closed held-mode
// store (which must not fall back to writing by path). A nil receiver is
// refused too, so the write methods can be called on an unset store.
func (s *Store) usable() error {
	if s == nil || s.Root == "" {
		return blobErrorf("blob store: no plan directory")
	}
	if s.held != nil && s.held.closed {
		return blobErrorf("blob store %s: closed", DirLabel(s.Root))
	}
	return nil
}

// blobErrorf formats a blobError; the format and its %w wrap the cause, and
// must not carry a "plan: " prefix (blobError adds it once).
func blobErrorf(format string, args ...any) error {
	return &blobError{err: fmt.Errorf(format, args...)}
}

// BlobRefFor returns the blob ref that WriteFile/WriteTree/WriteGlob would
// use for name, without writing anything. Callers that need to predict a ref
// ahead of a write — such as api/plan.go's collision guard, which must catch
// an unexpected ref clash before it silently overwrites a different
// resource's blob — call this instead of duplicating the sanitize-and-prefix
// logic.
func BlobRefFor(name string) (string, error) {
	safe := sanitizeBlobName(name)
	if safe == "" {
		return "", blobErrorf("empty blob name")
	}
	ref := "blobs/" + safe
	if err := validateBlobRef(ref); err != nil {
		return "", err
	}
	return ref, nil
}

func validateBlobRef(ref string) error {
	if ref == "" {
		return blobErrorf("missing blob id")
	}
	if filepath.IsAbs(ref) || strings.Contains(ref, "..") {
		return blobErrorf("invalid blob id %q", ref)
	}
	slash := filepath.ToSlash(ref)
	if !strings.HasPrefix(slash, "blobs/") || slash == "blobs/" {
		return blobErrorf("invalid blob id %q", ref)
	}
	return nil
}

func sanitizeBlobName(name string) string {
	name = strings.TrimSpace(name)
	name = filepath.Base(filepath.Clean(name))
	var b strings.Builder
	for _, r := range name {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r), r == '-', r == '_', r == '.':
			b.WriteRune(r)
		case r == ' ', r == '/', r == '\\', r == ':':
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-.")
	if out == "" || out == "." || out == ".." {
		return "blob"
	}
	return out
}
