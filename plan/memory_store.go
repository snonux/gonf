package plan

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

// BlobStore packages plan blob sidecars (disk or memory).
type BlobStore interface {
	WriteFile(name string, data []byte) (string, error)
	WriteTree(name, srcDir string) (string, error)
	WriteGlob(name, pattern string) (string, error)
}

// MemoryStore keeps blob bytes in RAM so push can avoid writing plan
// artifacts to the controller disk.
type MemoryStore struct {
	// files maps blob ref (blobs/name) to single-file content.
	files map[string][]byte
	// trees maps blob ref to relative path → file content (dirs implied).
	trees map[string]map[string][]byte
}

// NewMemoryStore returns an empty in-memory blob store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		files: make(map[string][]byte),
		trees: make(map[string]map[string][]byte),
	}
}

var _ BlobStore = (*MemoryStore)(nil)
var _ BlobStore = (*Store)(nil)

// WriteFile stores data under blobs/<name>.
func (m *MemoryStore) WriteFile(name string, data []byte) (string, error) {
	if m == nil {
		return "", fmt.Errorf("plan: blob store: nil memory store")
	}
	ref, err := memoryRef(name)
	if err != nil {
		return "", err
	}
	cp := make([]byte, len(data))
	copy(cp, data)
	delete(m.trees, ref)
	m.files[ref] = cp
	return ref, nil
}

// WriteTree copies srcDir into an in-memory tree at blobs/<name>/.
func (m *MemoryStore) WriteTree(name, srcDir string) (string, error) {
	if m == nil {
		return "", fmt.Errorf("plan: blob store: nil memory store")
	}
	info, err := os.Stat(srcDir)
	if err != nil {
		return "", fmt.Errorf("plan: package tree %s: %w", srcDir, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("plan: package tree %s: not a directory", srcDir)
	}
	ref, err := memoryRef(name)
	if err != nil {
		return "", err
	}
	tree := make(map[string][]byte)
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
		relSlash := filepath.ToSlash(rel)
		switch {
		case entry.Type()&fs.ModeSymlink != 0:
			// Symlink targets are not preserved in memory trees; skip like a no-op
			// would lose data — read through to regular file when possible.
			targetInfo, err := os.Stat(path)
			if err != nil || !targetInfo.Mode().IsRegular() {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			tree[relSlash] = data
			return nil
		case entry.IsDir():
			return nil
		default:
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			tree[relSlash] = data
			return nil
		}
	})
	if err != nil {
		return "", fmt.Errorf("plan: package tree into %q: %w", ref, err)
	}
	delete(m.files, ref)
	m.trees[ref] = tree
	return ref, nil
}

// WriteGlob copies basename matches of pattern into an in-memory tree.
func (m *MemoryStore) WriteGlob(name, pattern string) (string, error) {
	if m == nil {
		return "", fmt.Errorf("plan: blob store: nil memory store")
	}
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return "", fmt.Errorf("plan: package glob %q: %w", pattern, err)
	}
	ref, err := memoryRef(name)
	if err != nil {
		return "", err
	}
	tree := make(map[string][]byte)
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
		data, err := os.ReadFile(match)
		if err != nil {
			return "", err
		}
		tree[filepath.Base(match)] = data
	}
	delete(m.files, ref)
	m.trees[ref] = tree
	return ref, nil
}

// HasBlobs reports whether any blob was packaged.
func (m *MemoryStore) HasBlobs() bool {
	if m == nil {
		return false
	}
	return len(m.files) > 0 || len(m.trees) > 0
}

// Refs returns sorted blob refs stored in memory.
func (m *MemoryStore) Refs() []string {
	if m == nil {
		return nil
	}
	seen := make(map[string]struct{}, len(m.files)+len(m.trees))
	for ref := range m.files {
		seen[ref] = struct{}{}
	}
	for ref := range m.trees {
		seen[ref] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for ref := range seen {
		out = append(out, ref)
	}
	sort.Strings(out)
	return out
}

// FileBlob returns single-file blob bytes for ref.
func (m *MemoryStore) FileBlob(ref string) ([]byte, bool) {
	if m == nil {
		return nil, false
	}
	data, ok := m.files[ref]
	return data, ok
}

// TreeBlob returns the relative-path map for a tree blob ref.
func (m *MemoryStore) TreeBlob(ref string) (map[string][]byte, bool) {
	if m == nil {
		return nil, false
	}
	tree, ok := m.trees[ref]
	return tree, ok
}

func memoryRef(name string) (string, error) {
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
