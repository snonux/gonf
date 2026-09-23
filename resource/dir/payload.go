package dir

import "github.com/snonux/gonf/resource"

// SyncPayload is the "sync_dir" kind's exclusive plan-draft fields (task
// w62 Layer 1: splitting resource.PlanDraft's god struct into per-kind
// payloads). Prune stays a flat resource.PlanDraft field because the plain
// "dir" kind (also handled by this package) reuses it with the identical
// meaning; Blob stays flat because it is filled in later by api's
// packageDraft, after ToOp returns, and file's large content reuses the
// same field. The "dir" and "ensure_dir" kinds this package also handles
// have no exclusive fields at all, so they need no payload type.
// (*Dir).planDraft sets PlanDraft.Payload to this type for a "sync_dir"
// draft; the sync_dir plan.Handler's ToOp type-asserts it back, and the
// cross-kind packaging code (api/packager.go, internal/testapply) that
// reads a draft's source before ToOp is called also type-asserts it to
// find SourceDir/SourceGlob.
type SyncPayload struct {
	// SourceDir is a controller-local directory to package as a blob tree;
	// it also carries the recipe's DECLARED source directory (the glob
	// pattern's directory for the glob flavor) onto the op's source_dir
	// field, so destination apply renders tree .tmpl files' {{.Param}}
	// from it instead of the ephemeral blob path.
	SourceDir string
	// SourceGlob is a controller-local glob to package as a flat blob dir.
	SourceGlob string
	// FileMode is an octal permission string for files copied from
	// SourceDir or SourceGlob (same format as Mode).
	FileMode string
}

// Clone returns p unchanged: it has no reference fields to deep-copy.
// Implements resource.DraftPayload.
func (p SyncPayload) Clone() resource.DraftPayload { return p }

// SourceDirGlob returns p's SourceDir and SourceGlob. Implements
// resource.SourceDirPayload, so kind-neutral packaging code
// (api/packager.go, internal/testapply) can find them without importing
// this package back.
func (p SyncPayload) SourceDirGlob() (sourceDir, sourceGlob string) {
	return p.SourceDir, p.SourceGlob
}
