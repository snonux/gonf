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
func (s *Store) WriteFile(name string, data []byte) (string, error) {
	if s == nil || s.Root == "" {
		return "", fmt.Errorf("plan: blob store: no plan directory")
	}
	ref, abs, err := s.prepareRef(name)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
		return "", fmt.Errorf("plan: mkdir blobs: %w", err)
	}
	if err := os.WriteFile(abs, data, 0o600); err != nil {
		return "", fmt.Errorf("plan: write blob %q: %w", ref, err)
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
	if s == nil || s.Root == "" {
		return "", fmt.Errorf("plan: blob store: no plan directory")
	}
	entries, err := scanTree(srcDir)
	if err != nil {
		return "", err
	}
	ref, abs, err := s.prepareRef(name)
	if err != nil {
		return "", err
	}
	if err := os.RemoveAll(abs); err != nil {
		return "", fmt.Errorf("plan: clear blob %q: %w", ref, err)
	}
	if err := os.MkdirAll(abs, 0o700); err != nil {
		return "", fmt.Errorf("plan: mkdir blob %q: %w", ref, err)
	}
	if err := materializeEntries(abs, entries); err != nil {
		return "", fmt.Errorf("plan: package tree into %q: %w", ref, err)
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
	if s == nil || s.Root == "" {
		return "", fmt.Errorf("plan: blob store: no plan directory")
	}
	entries, err := scanGlob(pattern)
	if err != nil {
		return "", err
	}
	ref, abs, err := s.prepareRef(name)
	if err != nil {
		return "", err
	}
	if err := os.RemoveAll(abs); err != nil {
		return "", fmt.Errorf("plan: clear blob %q: %w", ref, err)
	}
	if err := os.MkdirAll(abs, 0o700); err != nil {
		return "", fmt.Errorf("plan: mkdir blob %q: %w", ref, err)
	}
	if err := materializeEntries(abs, entries); err != nil {
		return "", fmt.Errorf("plan: package glob into %q: %w", ref, err)
	}
	return ref, nil
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
		return "", fmt.Errorf("plan: empty blob name")
	}
	ref := "blobs/" + safe
	if err := validateBlobRef(ref); err != nil {
		return "", err
	}
	return ref, nil
}

func validateBlobRef(ref string) error {
	if ref == "" {
		return fmt.Errorf("plan: missing blob id")
	}
	if filepath.IsAbs(ref) || strings.Contains(ref, "..") {
		return fmt.Errorf("plan: invalid blob id %q", ref)
	}
	slash := filepath.ToSlash(ref)
	if !strings.HasPrefix(slash, "blobs/") || slash == "blobs/" {
		return fmt.Errorf("plan: invalid blob id %q", ref)
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
