package plan

import (
	"fmt"
	"io"
	"io/fs"
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

// WriteTree copies srcDir into blobs/<name>/ and returns the ref.
func (s *Store) WriteTree(name, srcDir string) (string, error) {
	if s == nil || s.Root == "" {
		return "", fmt.Errorf("plan: blob store: no plan directory")
	}
	info, err := os.Stat(srcDir)
	if err != nil {
		return "", fmt.Errorf("plan: package tree %s: %w", srcDir, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("plan: package tree %s: not a directory", srcDir)
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
	err = filepath.WalkDir(srcDir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		dst := filepath.Join(abs, rel)
		switch {
		case entry.Type()&fs.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(target, dst)
		case entry.IsDir():
			return os.MkdirAll(dst, 0o700)
		default:
			return copyFile(path, dst)
		}
	})
	if err != nil {
		return "", fmt.Errorf("plan: package tree into %q: %w", ref, err)
	}
	return ref, nil
}

// WriteGlob copies basename matches of pattern into blobs/<name>/ (flat).
func (s *Store) WriteGlob(name, pattern string) (string, error) {
	if s == nil || s.Root == "" {
		return "", fmt.Errorf("plan: blob store: no plan directory")
	}
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return "", fmt.Errorf("plan: package glob %q: %w", pattern, err)
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
	for _, match := range matches {
		info, err := os.Lstat(match)
		if err != nil {
			return "", fmt.Errorf("plan: package glob match %s: %w", match, err)
		}
		if info.IsDir() {
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 {
			targetInfo, err := os.Stat(match)
			if err != nil || targetInfo.IsDir() || !targetInfo.Mode().IsRegular() {
				continue
			}
		} else if !info.Mode().IsRegular() {
			continue
		}
		dst := filepath.Join(abs, filepath.Base(match))
		if err := copyFile(match, dst); err != nil {
			return "", fmt.Errorf("plan: package glob into %q: %w", ref, err)
		}
	}
	return ref, nil
}

func (s *Store) prepareRef(name string) (ref, abs string, err error) {
	safe := sanitizeBlobName(name)
	if safe == "" {
		return "", "", fmt.Errorf("plan: empty blob name")
	}
	ref = "blobs/" + safe
	if err := validateBlobRef(ref); err != nil {
		return "", "", err
	}
	return ref, filepath.Join(s.Root, filepath.FromSlash(ref)), nil
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

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}
