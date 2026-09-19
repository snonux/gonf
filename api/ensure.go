package api

import (
	"os"

	"github.com/snonux/gonf/internal/logger"
	"github.com/snonux/gonf/resource"
	"github.com/snonux/gonf/resource/dir"
	"github.com/snonux/gonf/resource/options"
)

// EnsureDir registers a Dir resource only when path is missing or is not
// already a directory (symlink-to-directory counts as present). When skipped,
// returns an empty Multi so DependsOn remains safe.
//
// In plan-record mode, emits an ensure_dir recipe instead of probing the
// controller filesystem (destination apply interprets the recipe). A draft
// build failure (invalid options) fails fast via logger.Fatal at record time.
func EnsureDir(path string, opts ...options.DirOption) Resource {
	p := Expand(path)
	if resource.PlanDraftRecording() {
		draft, err := dir.EnsurePlanDraft(p, opts...)
		if err != nil {
			logger.Fatal("EnsureDir %s: %v", p, err)
		}
		resource.RecordPlanDraft(draft)
		return resource.Multi(nil)
	}
	info, err := os.Stat(p)
	if err == nil && info.IsDir() {
		return resource.Multi(nil)
	}
	return Dir(p, opts...)
}
