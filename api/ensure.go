package api

import (
	"os"

	"github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/resource"
)

// EnsureDir registers a Dir resource only when path is missing or is not
// already a directory (symlink-to-directory counts as present). When skipped,
// returns an empty Multi so DependsOn remains safe.
func EnsureDir(path string, opts ...options.Option) Resource {
	p := Expand(path)
	info, err := os.Stat(p)
	if err == nil && info.IsDir() {
		return resource.Multi(nil)
	}
	return Dir(p, opts...)
}
