package plan

import (
	"fmt"
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
	// trees maps blob ref to the neutral tree manifest (entries sorted
	// by Rel; see BlobEntry). Trees carry files by content, directories
	// (empty ones included), and symlinks raw — the same manifest the
	// disk Store packages, so local and remote transports cannot diverge.
	trees map[string][]BlobEntry
}

// NewMemoryStore returns an empty in-memory blob store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		files: make(map[string][]byte),
		trees: make(map[string][]BlobEntry),
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

// WriteTree packages srcDir into an in-memory tree at blobs/<name>/. The
// tree is packaged through scanTree: directories (empty ones included) and
// symlinks (raw target, dangling included — never read through) are
// preserved as themselves, regular files by content; other file types fail
// loudly. This is the same manifest the disk Store packages.
func (m *MemoryStore) WriteTree(name, srcDir string) (string, error) {
	if m == nil {
		return "", fmt.Errorf("plan: blob store: nil memory store")
	}
	entries, err := scanTree(srcDir)
	if err != nil {
		return "", err
	}
	ref, err := memoryRef(name)
	if err != nil {
		return "", err
	}
	delete(m.files, ref)
	m.trees[ref] = entries
	return ref, nil
}

// WriteGlob packages basename matches of pattern into an in-memory flat
// tree: regular files by content, symlinks preserved raw, directories and
// other file types skipped — the same file+symlink policy the disk Store
// applies.
func (m *MemoryStore) WriteGlob(name, pattern string) (string, error) {
	if m == nil {
		return "", fmt.Errorf("plan: blob store: nil memory store")
	}
	entries, err := scanGlob(pattern)
	if err != nil {
		return "", err
	}
	ref, err := memoryRef(name)
	if err != nil {
		return "", err
	}
	delete(m.files, ref)
	m.trees[ref] = entries
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

// TreeBlob returns the neutral manifest entries (sorted by Rel) for a tree
// blob ref: BlobFile entries carry Data, BlobDir entries mark directories,
// and BlobSymlink entries carry the raw Target.
func (m *MemoryStore) TreeBlob(ref string) ([]BlobEntry, bool) {
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
