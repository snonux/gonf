package api

import (
	"encoding/base64"
	"fmt"
	"os"

	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
)

// syncDirSource returns d's sync_dir SourceDir and SourceGlob, or "", "" for
// a draft whose kind is not sync_dir. They moved off resource.PlanDraft's
// flat fields into resource/dir's SyncPayload (task w62 Layer 1); packaging
// code that runs before/alongside plan.Handler.ToOp (which only a
// resource/<kind> package's own planwire.go would otherwise type-assert)
// finds them through the kind-neutral resource.SourceDirPayload interface
// instead of a direct field read, so this package need not import
// resource/dir just for this.
//
// The d.Kind == "sync_dir" check runs BEFORE the type assertion (task 0e2):
// w62 Layer 1 left the assertion alone consulting whatever concrete type
// d.Payload happened to hold, with no discriminator. That is structural
// typing, not the deliberate opt-in the flat-field era had — any future
// payload that grows a like-named SourceDirGlob() for its own, unrelated
// reason would silently have its accessor called and its return values
// packaged as a blob into THIS draft's op, even though its kind never meant
// to carry a source directory. Gating on Kind restores the "only a kind that
// deliberately opted in" guarantee; TestSourcePayloadFitness (api/
// plan_fitness_test.go) pins which payload types are allowed to implement
// the marker at all, as defense in depth alongside this gate.
func syncDirSource(d resource.PlanDraft) (sourceDir, sourceGlob string) {
	if d.Kind != "sync_dir" {
		return "", ""
	}
	if sp, ok := d.Payload.(resource.SourceDirPayload); ok {
		return sp.SourceDirGlob()
	}
	return "", ""
}

// sourceFilePath returns d's "file"/"ensure_file"-kind SourcePath, or "" for
// any other draft. It moved off resource.PlanDraft's flat field into
// resource/file's Payload (task w62 Layer 1); found through the kind-neutral
// resource.SourceFilePayload interface for the same reason as
// syncDirSource above.
//
// The d.Kind check runs BEFORE the type assertion, for the same reason as
// syncDirSource's above (task 0e2): consulting the marker interface against
// whatever d.Payload happens to hold, with no discriminator, let a future
// unrelated payload's like-named SourceFilePath() silently leak controller-
// local file bytes into its own kind's op. "ensure_file" is included because
// resource/file's (*File).planDraft fills Payload with the same file.Payload
// type for both kinds (ensureFileHandler.ToOp just never reads
// SourcePath, so it is always "" there in practice) — excluding it here
// would not add safety, only asymmetry with the type-level allow-list
// TestSourcePayloadFitness pins.
func sourceFilePath(d resource.PlanDraft) string {
	if d.Kind != "file" && d.Kind != "ensure_file" {
		return ""
	}
	if sp, ok := d.Payload.(resource.SourceFilePayload); ok {
		return sp.SourceFilePath()
	}
	return ""
}

// draftPackager is the explicit context one packaging pass lowers drafts in:
// the blob store large sources go to, the blob refs the pass has already
// claimed (guardBlobRef), whether an enclosing Privileged() task body elevates
// every op (draftToOp), and the task being recorded, which names the recipe in
// a draft error (draftError).
//
// It exists so packageDraft reads no hidden global state. A recording session
// hands out a packager built from its live state (recordingSession.packager);
// a local api.Apply, which is not a recording session, builds a fresh one
// (newDraftPackager), so nothing an earlier RecordPlan in the same process
// left in the session — a claimed blob ref, an elevate flag, a task name on
// the stack — can leak into (falsely collide with, elevate or mislabel) the
// Apply's ops, and Apply never has to reach into the session to reset it.
//
// Value semantics: a packager is copied freely. blobRefs is a map, so every
// copy made for one pass shares (and extends) the same claimed-ref set.
type draftPackager struct {
	store    plan.BlobStore    // nil: no blob staging available
	blobRefs map[string]string // blob ref → blobIdentityKey that claimed it
	elevate  bool              // fold elevation into every op
	task     string            // task body being recorded; "" outside one
}

// newDraftPackager returns the packager for a packaging pass outside any
// recording session (api.Apply): a fresh claimed-ref set, no inherited
// elevation and no task name. store may be nil when no draft needs blobs.
func newDraftPackager(store plan.BlobStore) draftPackager {
	return draftPackager{store: store, blobRefs: map[string]string{}}
}

// packageDraft lowers d to its plan op (draftToOp, which also carries an
// explicit WithSensitive), packages its source data (packageContent) and
// marks the op sensitive when it carries a resolved secret — or refuses it
// when a strong secret sits in one of its identities (markSensitive; a
// packaged file source is scanned from the bytes read here, since a
// blob-backed op no longer carries them).
func (p draftPackager) packageDraft(d resource.PlanDraft) (plan.Op, error) {
	op, source, err := p.packageContent(d)
	if err != nil {
		return op, err
	}
	if err := markSensitive(&op, source, p.task); err != nil {
		return op, err
	}
	return op, nil
}

// packageContent is packageDraft without the sensitivity scan: a file
// source inline as content_b64 when it fits, otherwise — like a sync_dir
// tree or glob — as a blob in p.store. It also returns a file source's
// bytes (nil for other drafts).
func (p draftPackager) packageContent(d resource.PlanDraft) (plan.Op, []byte, error) {
	op, err := p.draftToOp(d)
	if err != nil {
		return plan.Op{}, nil, err
	}
	name := blobName(d)
	sourceDir, sourceGlob := syncDirSource(d)
	switch {
	case sourceFilePath(d) != "":
		return p.packageSourceFile(op, d, name)
	case sourceGlob != "":
		op, err = p.packageBlob(op, d, name, sourceGlob, p.writeGlob)
		return op, nil, err
	case sourceDir != "":
		op, err = p.packageBlob(op, d, name, sourceDir, p.writeTree)
		return op, nil, err
	}
	return op, nil, nil
}

// packageSourceFile packages a file source: inline as content_b64 up to
// plan.MaxInlineContent, otherwise as a blob, which needs a store. It
// returns the source bytes as well.
func (p draftPackager) packageSourceFile(op plan.Op, d resource.PlanDraft, name string) (plan.Op, []byte, error) {
	path := sourceFilePath(d)
	data, err := os.ReadFile(path)
	if err != nil {
		return op, nil, fmt.Errorf("package file %s: %w", path, err)
	}
	if len(data) <= plan.MaxInlineContent {
		op.ContentB64 = base64.StdEncoding.EncodeToString(data)
		op.Blob = ""
		return op, data, nil
	}
	if p.store == nil {
		return op, nil, fmt.Errorf("package file %s: exceeds inline limit and no plan dir for blobs", path)
	}
	if err := p.guardBlobRef(name, d); err != nil {
		return op, nil, err
	}
	ref, err := p.store.WriteFile(name, data)
	if err != nil {
		return op, nil, err
	}
	op.Blob = ref
	op.ContentB64 = ""
	return op, data, nil
}

// packageBlob packages a sync_dir source (a tree or a glob, named by src) as
// a blob through write; blob packaging always needs a store.
func (p draftPackager) packageBlob(op plan.Op, d resource.PlanDraft, name, src string, write func(name, src string) (string, error)) (plan.Op, error) {
	if p.store == nil {
		return op, fmt.Errorf("package sync_dir %s: plan dir required for blob packaging", src)
	}
	if err := p.guardBlobRef(name, d); err != nil {
		return op, err
	}
	ref, err := write(name, src)
	if err != nil {
		return op, err
	}
	op.Blob = ref
	return op, nil
}

// writeGlob and writeTree adapt the store's two sync_dir writers to
// packageBlob's write parameter. They are only called once packageBlob has
// checked that p.store is set.
func (p draftPackager) writeGlob(name, glob string) (string, error) {
	return p.store.WriteGlob(name, glob)
}

func (p draftPackager) writeTree(name, dir string) (string, error) {
	return p.store.WriteTree(name, dir)
}

// guardBlobRef predicts the blob ref that store.Write{File,Tree,Glob} will
// produce for name and fails loudly if a *different* resource already
// claimed that exact ref earlier in this packaging pass (for a recording
// session: anywhere in the session). blobName's hash suffix should already
// make that impossible; this is defense in depth so a regression here fails
// RecordPlan instead of silently corrupting a blob (the data-loss failure
// mode this whole fix exists to close). A resource recorded twice with the
// same identity (e.g. a diamond-included task body running again in a
// disjoint branch) legitimately reuses its own ref, so that case is not an
// error.
func (p draftPackager) guardBlobRef(name string, d resource.PlanDraft) error {
	ref, err := plan.BlobRefFor(name)
	if err != nil {
		return err
	}
	identity := blobIdentityKey(d)
	if prior, ok := p.blobRefs[ref]; ok {
		if prior == identity {
			return nil
		}
		return fmt.Errorf("RecordPlan: blob ref %q collision: already packaged for %q, now requested for %q (this should be impossible after blobName hashing; please report)",
			ref, prior, identity)
	}
	p.blobRefs[ref] = identity
	return nil
}

// draftError points a handler's record-time rejection at the recipe: the
// task being recorded (p.task; a local api.Apply has none) and the draft's
// resource ID, as "RecordPlan: [task %q: ]draft %q: <handler error>". The
// "RecordPlan: draft %q:" part matches draftToOp's own errors, which name no
// task; the task part mirrors checkUnrecordedDrafts. The handler's error is
// wrapped, so errors.Is/As still see it, and handlers must not add their own
// ID prefix.
func (p draftPackager) draftError(d resource.PlanDraft, err error) error {
	if p.task == "" {
		return fmt.Errorf("RecordPlan: draft %q: %w", d.ID, err)
	}
	return fmt.Errorf("RecordPlan: task %q: draft %q: %w", p.task, d.ID, err)
}

// draftToOp lowers a resource draft to a plan op line by delegating to the
// draft kind's registered plan.Handler (see plan/handler.go): the resource
// package owns its own wire form and this method only folds in the
// packager's elevate flag and the draft's explicit sensitivity. Every
// resource kind registers a Handler (see docs/plan.md, "Adding a resource
// kind"), so an unmapped kind is always a programming error (typo, or a new
// resource kind that forgot to register) and fails the record loudly here
// instead of silently forwarding an unknown op to the wire, where it would
// only blow up at remote apply time.
func (p draftPackager) draftToOp(d resource.PlanDraft) (plan.Op, error) {
	h, ok := plan.HandlerFor(plan.Kind(d.Kind))
	if !ok {
		return plan.Op{}, fmt.Errorf("RecordPlan: draft %q: unknown draft kind %q (no registered plan.Handler; see docs/plan.md kind checklist)",
			d.ID, d.Kind)
	}
	op, err := h.ToOp(d)
	if err != nil {
		return plan.Op{}, p.draftError(d, err)
	}
	op.Elevate = d.Elevate || p.elevate
	// An explicit WithSensitive (d.Sensitive) marks the op like a secret the
	// scan found (markSensitive, later in packageDraft); handlers never set
	// Sensitive themselves, so the flag is folded in here once for every
	// kind. It only ever adds: an unmarked draft lowers exactly as before.
	op.Sensitive = op.Sensitive || d.Sensitive
	if !plan.IsKnownKind(op.Op) {
		return op, fmt.Errorf("RecordPlan: draft %q: kind %q lowers to undeclared plan kind %q (missing from plan.AllKinds)",
			d.ID, d.Kind, op.Op)
	}
	return op, nil
}
