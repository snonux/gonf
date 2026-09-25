package api

import (
	"os"
	"path/filepath"

	"github.com/snonux/gonf/internal/declerr"
	"github.com/snonux/gonf/resource"
	"github.com/snonux/gonf/resource/link"
	"github.com/snonux/gonf/resource/options"
)

// LinkIfExists creates a symlink at path → target when target exists;
// otherwise ensures path is absent (NoLink).
//
// In plan-record mode, emits a link_if_exists recipe instead of probing the
// controller filesystem (destination apply interprets the recipe).
func LinkIfExists(path, target string, opts ...options.LinkOption) Resource {
	p := Expand(path)
	t := Expand(target)
	if resource.PlanDraftRecording() {
		// No DependsOn state exists here: record mode ignores opts (the recipe
		// is evaluated on the destination), so the draft carries no deps.
		resource.RecordPlanDraft(resource.PlanDraft{
			Kind:    "link_if_exists",
			Path:    p,
			Payload: link.IfExistsPayload{Target: t},
			ID:      resource.FormatID("LinkIfExists", p),
		})
		return resource.Multi(nil)
	}
	// Direct mode: expand a ${HOME} target (DestHome) against this host,
	// the destination; the Link keeps the token for its own handler.
	if _, err := os.Stat(localPath(t)); err != nil {
		return NoLink(p, opts...)
	}
	return Link(p, append([]options.LinkOption{options.WithSymlink(t)}, opts...)...)
}

// SymlinkMap registers LinkIfExists for each name/target pair under parent.
// pairs must be an even-length list: name1, target1, name2, target2, ...
// An odd-length list is recipe misuse: it is reported as a declaration error
// (internal/declerr) and no link is declared.
func SymlinkMap(parent string, pairs ...string) {
	if len(pairs)%2 != 0 {
		declerr.Reportf("SymlinkMap: odd number of elements (%d)", len(pairs))
		return
	}
	base := Expand(parent)
	for i := 0; i < len(pairs); i += 2 {
		name, target := pairs[i], pairs[i+1]
		LinkIfExists(filepath.Join(base, name), target)
	}
}
