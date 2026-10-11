package plan

import "fmt"

// FilteredBlobStore extends source packaging with exclusions applied before
// reading file bytes. Store and MemoryStore implement it. A custom BlobStore
// must implement it before a recipe with exclusions can be recorded into it.
type FilteredBlobStore interface {
	WriteTreeFiltered(name, srcDir string, exclude []string) (string, error)
	WriteGlobFiltered(name, pattern string, exclude []string) (string, error)
}

// WriteSyncSource packages the tree or glob source of a sync_dir operation.
// With exclusions it requires a FilteredBlobStore, refusing instead of silently
// packaging excluded bytes through a legacy store. A glob takes precedence.
func WriteSyncSource(store BlobStore, name, sourceDir, sourceGlob string, exclude []string) (string, error) {
	if store == nil {
		return "", fmt.Errorf("package sync source: nil blob store")
	}
	if len(exclude) == 0 {
		if sourceGlob != "" {
			return store.WriteGlob(name, sourceGlob)
		}
		return store.WriteTree(name, sourceDir)
	}
	filtered, ok := store.(FilteredBlobStore)
	if !ok {
		return "", fmt.Errorf("package sync source: blob store does not support exclusions")
	}
	if sourceGlob != "" {
		return filtered.WriteGlobFiltered(name, sourceGlob, exclude)
	}
	return filtered.WriteTreeFiltered(name, sourceDir, exclude)
}
