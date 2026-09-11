package api

import (
	"log"
	"os"
	"path/filepath"

	"github.com/snonux/gonf/api/options"
)

// LinkIfExists creates a symlink at path → target when target exists;
// otherwise ensures path is absent (NoLink).
func LinkIfExists(path, target string, opts ...options.Option) Resource {
	p := Expand(path)
	t := Expand(target)
	if _, err := os.Stat(filepath.Clean(t)); err != nil {
		return NoLink(p, opts...)
	}
	return Link(p, append([]options.Option{options.WithSymlink(t)}, opts...)...)
}

// SymlinkMap registers LinkIfExists for each name/target pair under parent.
// pairs must be an even-length list: name1, target1, name2, target2, ...
func SymlinkMap(parent string, pairs ...string) {
	if len(pairs)%2 != 0 {
		log.Fatalf("SymlinkMap: odd number of elements (%d)", len(pairs))
	}
	base := Expand(parent)
	for i := 0; i < len(pairs); i += 2 {
		name, target := pairs[i], pairs[i+1]
		LinkIfExists(filepath.Join(base, name), target)
	}
}
