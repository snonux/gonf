package file

import (
	"encoding/json"
	"slices"

	"github.com/snonux/gonf/resource"
)

// Payload is the "file" kind's exclusive plan-draft fields (task w62
// Layer 1: splitting resource.PlanDraft's god struct into per-kind
// payloads). Blob stays a flat resource.PlanDraft field because sync_dir
// reuses it too (and because it is filled in later by api's packageDraft,
// after ToOp returns, like sync_dir's Blob). The "ensure_file" kind this
// package also handles has no exclusive fields of its own, but
// (*File).planDraft still fills Payload unconditionally for it (matching
// the previous flat-field behaviour, which never special-cased ensure_file
// either) — ensureFileHandler.ToOp simply never reads it.
// (*File).planDraft sets PlanDraft.Payload to this type; the file
// plan.Handler's ToOp type-asserts it back.
type Payload struct {
	// ContentB64 is base64 file content.
	ContentB64 string
	// SourcePath is a controller-local file to package as content_b64 or a
	// blob.
	SourcePath string
	// HasContent marks that content was explicitly configured (WithContent
	// or WithSource), even when the resulting bytes are empty. It lets
	// apply tell a legitimately empty file apart from an op that is
	// missing content data outright (a record-time bug): only the latter
	// should fail loudly with "missing content_b64 and blob".
	HasContent bool
	// Template marks that the content must be rendered as a text/template
	// on the destination: the recipe's source or path ended in ".tmpl".
	Template bool
	// TemplateParam is the declared source path used as the template's
	// {{.Param}} default when Template is set.
	TemplateParam string
	// TemplateData is the JSON encoding of the value supplied by
	// WithTemplateData, taken when the option was applied (File.
	// SetTemplateData), so a recipe that later mutates or reuses its value
	// cannot change what api.Apply or RecordPlan lower.
	TemplateData json.RawMessage
	// TemplateDataErr is the JSON encoding error when the supplied value is
	// not JSON-compatible (TemplateData is then empty). The file handler's
	// ToOp refuses such a draft, wrapping this error, so RecordPlan and
	// api.Apply both fail on it and callers can still errors.As the
	// encoding/json error.
	TemplateDataErr error
	// TemplateDataSet reports that WithTemplateData was given, so
	// TemplateData is put on the op even when the supplied value was nil
	// ("null").
	TemplateDataSet bool
	// ValidationBin and ValidationArgs describe an optional argv validator
	// for a content-managed file. ValidationArgs retains CandidatePath as a
	// typed wire token; destination apply supplies the private candidate
	// filename.
	ValidationBin  string
	ValidationArgs []string
	// AddLines appends lines to a file when missing (line-in-file).
	AddLines []string
	// RemoveLines removes matching lines from a file.
	RemoveLines []string
	// KeyedLines are WithKeyedLine edits: each owns the one line of the
	// file that starts with its Key (see resource/file/lineedit.go).
	KeyedLines []resource.KeyedLine
	// Blocks are WithBlock managed blocks: each owns the lines between its
	// markers (see resource/file/blockedit.go).
	Blocks []resource.Block
}

// Clone returns a deep copy of p: every slice gets its own backing storage.
// TemplateDataErr is the one field Clone shares (mirroring
// resource.PlanDraft.Clone's own former documented exception): it is an
// immutable error value nothing writes through, not storage that mutation
// could corrupt. Implements resource.DraftPayload.
func (p Payload) Clone() resource.DraftPayload {
	c := p
	c.TemplateData = slices.Clone(p.TemplateData)
	c.ValidationArgs = slices.Clone(p.ValidationArgs)
	c.AddLines = slices.Clone(p.AddLines)
	c.RemoveLines = slices.Clone(p.RemoveLines)
	c.KeyedLines = slices.Clone(p.KeyedLines)
	c.Blocks = cloneBlocks(p.Blocks)
	return c
}

// SourceFilePath returns p's SourcePath. Implements
// resource.SourceFilePayload, so kind-neutral packaging code
// (api/packager.go, internal/testapply) can find it without importing this
// package back.
func (p Payload) SourceFilePath() string { return p.SourcePath }
