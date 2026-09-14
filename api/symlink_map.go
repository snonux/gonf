package api

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/internal/logger"
	"github.com/snonux/gonf/resource"
)

// LinkIfExists creates a symlink at path → target when target exists;
// otherwise ensures path is absent (NoLink).
//
// In plan-record mode, emits a link_if_exists recipe instead of probing the
// controller filesystem (destination apply interprets the recipe).
func LinkIfExists(path, target string, opts ...options.Option) Resource {
	p := Expand(path)
	t := Expand(target)
	if resource.PlanDraftRecording() {
		// No DependsOn state exists here: record mode ignores opts (the recipe
		// is evaluated on the destination), so the draft carries no deps.
		resource.RecordPlanDraft(resource.PlanDraft{
			Kind:   "link_if_exists",
			Path:   p,
			Target: t,
			ID:     fmt.Sprintf("LinkIfExists[%s]", p),
		})
		return resource.Multi(nil)
	}
	if _, err := os.Stat(filepath.Clean(t)); err != nil {
		return NoLink(p, opts...)
	}
	return Link(p, append([]options.Option{options.WithSymlink(t)}, opts...)...)
}

// SymlinkMap registers LinkIfExists for each name/target pair under parent.
// pairs must be an even-length list: name1, target1, name2, target2, ...
// An odd-length list is recipe misuse and fails fast via logger.Fatal.
func SymlinkMap(parent string, pairs ...string) {
	if len(pairs)%2 != 0 {
		logger.Fatal("SymlinkMap: odd number of elements (%d)", len(pairs))
	}
	base := Expand(parent)
	for i := 0; i < len(pairs); i += 2 {
		name, target := pairs[i], pairs[i+1]
		LinkIfExists(filepath.Join(base, name), target)
	}
}
