package plan

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

// Store writes and resolves blob sidecars under Root/blobs/.
type Store struct {
	Root string
}

// NewStore returns a blob store rooted at planDir (the directory that will
// hold plan.jsonl alongside blobs/).
func NewStore(planDir string) *Store {
	return &Store{Root: planDir}
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

// WriteFile writes data as blobs/<name> (a single file) and returns the ref.
// The blobs directory is created 0700 when missing; one that already exists is
// verified, not rewritten (WritePrivateFile, see SecureDir): it must be ours and
// not writable by others or by a shared group (SecureDir's rule; your private
// group may write), so a blob never lands in a directory somebody else can
// modify, but a mode such as 0755 that the operator chose is kept. The blob
// file itself is always 0600. Every component of the path down to blobs/ is
// opened without following symlinks, so a store whose Root is reached through
// a symlinked ancestor is refused here (WriteTree and WriteGlob are more
// lenient about that, see secureBlobsDir).
func (s *Store) WriteFile(name string, data []byte) (string, error) {
	if err := s.usable(); err != nil {
		return "", err
	}
	ref, abs, err := s.prepareRef(name)
	if err != nil {
		return "", err
	}
	if err := writePrivateFile(filepath.Dir(abs), filepath.Base(abs), data); err != nil {
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
	ref, abs, err := s.prepareRef(name)
	if err != nil {
		return "", err
	}
	if err := s.secureBlobsDir(); err != nil {
		return "", blobErrorf("blob %q: %w", ref, err)
	}
	if err := os.RemoveAll(abs); err != nil {
		return "", blobErrorf("clear blob %q: %w", ref, err)
	}
	if err := os.MkdirAll(abs, 0o700); err != nil {
		return "", blobErrorf("mkdir blob %q: %w", ref, err)
	}
	if err := materializeEntries(abs, entries); err != nil {
		return "", blobErrorf("package tree into %q: %w", ref, err)
	}
	return ref, nil
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
	ref, abs, err := s.prepareRef(name)
	if err != nil {
		return "", err
	}
	if err := s.secureBlobsDir(); err != nil {
		return "", blobErrorf("blob %q: %w", ref, err)
	}
	if err := os.RemoveAll(abs); err != nil {
		return "", blobErrorf("clear blob %q: %w", ref, err)
	}
	if err := os.MkdirAll(abs, 0o700); err != nil {
		return "", blobErrorf("mkdir blob %q: %w", ref, err)
	}
	if err := materializeEntries(abs, entries); err != nil {
		return "", blobErrorf("package glob into %q: %w", ref, err)
	}
	return ref, nil
}

// secureBlobsDir applies SecureDir's directory policy to the blobs/ directory
// of the plan directory that WriteTree and WriteGlob are about to fill, which
// they would otherwise create with MkdirAll and never look at again. Exactly
// what is checked:
//
//   - <Root>/blobs itself, and only it: missing => created 0700 (exactly 0700,
//     whatever the umask); existing => it must not be a symlink, must be a
//     directory owned by the effective user, must not be writable by others
//     and must not be writable by a group other than the caller's private
//     group (checkDirAttrs), and it is left unmodified;
//   - <Root> is created when missing (0700 masked by the umask, like the
//     MkdirAll these writers used before) and otherwise not looked at, and
//     neither is any ancestor of it: they are followed through symlinks. This
//     is deliberately weaker than SecureDir's walk. The staging store
//     (api/stage.go) and the local-run directory (api/task.go) are made under
//     $TMPDIR, and a $TMPDIR reached through a symlink (macOS: /var ->
//     /private/var) is normal; the plan directory the operator names is
//     verified in full by SecureDir before anything is committed to it.
//
// WriteFile is stricter and unchanged: it writes through a descriptor of its
// directory that SecureDir opened by walking every component O_NOFOLLOW, so a
// symlinked ancestor of the store root is refused there.
//
// The guarantee is also weaker than WriteFile's in another respect, and
// deliberately stated as such: WriteFile writes through the descriptor of the
// directory it verified, so the check and the write are on the same
// directory. secureChildDir closes its descriptor when it returns, and
// WriteTree/WriteGlob then clear, create and fill the tree BY PATH
// (os.RemoveAll, os.MkdirAll, materializeEntries). It is therefore a check of
// the state at the time of the call, not a protection against blobs/ being
// swapped for a symlink or another directory between the check and the use.
// Closing that would need the tree written through the held descriptor too; it
// is not needed for the honest case this guards against, an unsafe leftover or
// pre-planted blobs/ directory. The tree itself (blobs/<name>) is gonf's own
// scratch space below blobs/: it is cleared and recreated 0700 on every write.
func (s *Store) secureBlobsDir() error {
	return secureChildDir(s.Root, "blobs")
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

// usable refuses a store without a plan directory. A nil receiver is refused
// too, so the write methods can be called on an unset store.
func (s *Store) usable() error {
	if s == nil || s.Root == "" {
		return blobErrorf("blob store: no plan directory")
	}
	return nil
}

// blobErrorf formats a blobError; the format and its %w wrap the cause, and
// must not carry a "plan: " prefix (blobError adds it once).
func blobErrorf(format string, args ...any) error {
	return &blobError{err: fmt.Errorf(format, args...)}
}

func (s *Store) prepareRef(name string) (ref, abs string, err error) {
	ref, err = BlobRefFor(name)
	if err != nil {
		return "", "", err
	}
	return ref, filepath.Join(s.Root, filepath.FromSlash(ref)), nil
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
