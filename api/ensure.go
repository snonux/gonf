package api

import (
	"os"

	"github.com/snonux/gonf/internal/declerr"
	"github.com/snonux/gonf/resource"
	"github.com/snonux/gonf/resource/dir"
	"github.com/snonux/gonf/resource/file"
	"github.com/snonux/gonf/resource/options"
)

// EnsureFile registers a preserve-content file resource. It creates an empty
// regular file when path is absent; an existing regular file keeps its bytes
// while explicitly configured mode, owner, and group converge. Unlike a
// controller-side existence check, it records an ensure_file plan operation
// so remote application makes the decision on the destination host.
func EnsureFile(path string, opts ...options.FileOption) Resource {
	return file.PresentEnsure(Expand(path), opts...)
}

// EnsureDir registers a Dir resource only when path is missing or is not
// already a directory (symlink-to-directory counts as present). When skipped,
// returns an empty Multi so DependsOn remains safe.
//
// In plan-record mode, emits an ensure_dir recipe instead of probing the
// controller filesystem (destination apply interprets the recipe). A draft
// build failure (invalid options) is reported as a declaration error
// (internal/declerr), which fails the record, and nothing is recorded.
func EnsureDir(path string, opts ...options.DirOption) Resource {
	p := Expand(path)
	if resource.PlanDraftRecording() {
		draft, err := dir.EnsurePlanDraft(p, opts...)
		if err != nil {
			declerr.Reportf("EnsureDir %s: %w", p, err)
			return resource.Multi(nil)
		}
		resource.RecordPlanDraft(draft)
		return resource.Multi(nil)
	}
	// Direct mode: this host is the destination, so its home resolves a
	// ${HOME} token (DestHome) here.
	info, err := os.Stat(localPath(p))
	if err == nil && info.IsDir() {
		return resource.Multi(nil)
	}
	return Dir(p, opts...)
}
