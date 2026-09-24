package api

import (
	"fmt"
	"strings"

	"github.com/snonux/gonf/plan"
)

// DeferredPlan is a plan recorded into memory whose output form is decided
// only after recording (RecordPlanDeferred, task 5b2): `gonf plan -o dir`
// seals a sensitive plan by default when an operator recipients file
// exists, and which ops are sensitive is known only once every task body
// ran. Recording into memory first keeps both outcomes honest: a sealed
// write never lands a plaintext blob on disk (the same guarantee an
// explicit `-seal` gives), and a plaintext write commits the very same
// blobs with the same rules RecordPlan's staging commit applies.
type DeferredPlan struct {
	// Ops is the recorded plan, already through RecordPlanTo's pre-flight.
	Ops []plan.Op
	// Blobs holds every blob the recording packaged, for sealing
	// (plan.EncodePush reads a plan.BlobReader) or for CommitBlobs.
	Blobs *plan.MemoryStore

	planDir string
}

// RecordPlanDeferred records the named tasks into memory, after the same
// up-front plan directory check RecordPlan runs (checkPlanDirUsable: an
// unusable planDir is refused before any task body runs, creating and
// changing nothing). Nothing is written to planDir: the caller either
// seals Ops/Blobs itself or calls CommitBlobs and writes plan.jsonl, as
// RecordPlan's callers do. An empty planDir means "." (the -o default),
// never "no plan directory": this path always ends in some write to it.
func RecordPlanDeferred(planID, planDir string, taskNames ...string) (*DeferredPlan, error) {
	if planDir == "" {
		planDir = "."
	}
	if err := checkPlanDirUsable(planDir); err != nil {
		return nil, fmt.Errorf("RecordPlan: plan dir: %w", err)
	}
	mem := plan.NewMemoryStore()
	ops, err := RecordPlanTo(planID, mem, taskNames...)
	if err != nil {
		return nil, err
	}
	return &DeferredPlan{Ops: ops, Blobs: mem, planDir: planDir}, nil
}

// CommitBlobs copies every blob the plan references from memory into the
// plan directory, with exactly the rules and error wording of RecordPlan's
// commit (commitBlobs: the directory is verified and held, re-checked for
// writability, and every blob is written relative to it). A plan without
// blobs writes nothing, as with RecordPlan.
func (d *DeferredPlan) CommitBlobs() error {
	return commitBlobs(d.Ops, d.planDir, func(dest *plan.Store, ref string) error {
		return copyMemoryBlob(d.Blobs, dest, ref)
	})
}

// copyMemoryBlob is copyStagedBlob for a blob held in a MemoryStore: a
// single-file blob through dest.WriteFile, a tree or glob blob through
// dest.WriteEntries with the manifest recording already scanned, so the
// result matches what RecordPlan's staging commit writes byte for byte.
func copyMemoryBlob(mem *plan.MemoryStore, dest *plan.Store, ref string) error {
	name := strings.TrimPrefix(ref, "blobs/")
	var got string
	var err error
	if data, ok := mem.FileBlob(ref); ok {
		got, err = dest.WriteFile(name, data)
	} else if entries, ok := mem.TreeBlob(ref); ok {
		got, err = dest.WriteEntries(name, entries)
	} else {
		return fmt.Errorf("recorded blob %q is missing from memory", ref)
	}
	if err != nil {
		return err
	}
	if got != ref {
		return fmt.Errorf("recorded blob %q was written as %q", ref, got)
	}
	return nil
}
