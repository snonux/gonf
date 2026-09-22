// Package testapply applies the resources registered so far through the plan
// engine, for this module's tests.
//
// It replaces the retired resource.Apply repository path (task e72) in the
// resource/<kind> packages' own tests. Those tests live in the kind's package
// (package file, package dir, ...), which api imports, so they cannot call
// api.Apply without an import cycle. testapply imports only the kind-neutral
// core (plan and resource), so any test in the module can use it, and it
// runs the same engine api.Apply runs for an unprivileged recipe:
//
//  1. snapshot the registered plan drafts (resource.RegisteredPlanDrafts) and
//     refuse a registered resource without one, as api.Apply does;
//  2. lower each draft to its plan op through the kind's registered
//     plan.Handler, packaging file, glob and tree sources inline or as blobs
//     in a temporary plan directory;
//  3. run the whole-plan pre-flight api.Apply runs (plan.ValidateChunks over
//     the privilege chunks), so a dangling DependsOn or watch is refused
//     before anything is applied;
//  4. apply the ops with plan.Apply and the local host's facts, which orders
//     them by dependency and prints the outcome summary to stderr.
//
// Deliberately NOT mirrored: the secret scan and the privilege split. A test
// with an elevated op or a configured secret source goes through api.Apply
// (from package api or an external test package); Apply refuses an elevated
// op rather than silently running it unprivileged. api's
// TestTestapplyOpsMatchApply pins that the ops lowered here equal
// api.Apply's for a sample recipe, so the two cannot drift apart unnoticed.
//
// Behaviour the plan pre-flight makes unreachable (e.g. a change gate
// watching an ID nothing notes, refused as a dangling watch) is tested on
// the kind's direct Ensure path instead.
package testapply

import (
	"encoding/base64"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"

	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
)

// planID is the header ID of every plan this package builds.
const planID = "testapply"

// Apply lowers every registered resource to a plan op and applies the plan
// through plan.Apply, as described in the package comment. It returns nil
// without applying anything when nothing is registered.
func Apply() error {
	registered := resource.RegisteredIDs()
	if len(registered) == 0 {
		return nil
	}
	drafts := resource.RegisteredPlanDrafts()
	if err := requireDrafts(drafts, registered); err != nil {
		return err
	}
	planDir, err := os.MkdirTemp("", "gonf-testapply-*")
	if err != nil {
		return fmt.Errorf("testapply: temp plan dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(planDir) }()

	ops, err := Ops(drafts, plan.NewStore(planDir))
	if err != nil {
		return err
	}
	return applyOps(ops, planDir)
}

// Ops lowers drafts to a plan (a header followed by one op per draft, in
// draft order), staging sources too large for inline content in store. It
// is exported for the api parity test; tests normally call Apply.
func Ops(drafts []resource.PlanDraft, store plan.BlobStore) ([]plan.Op, error) {
	ops := []plan.Op{{Op: plan.KindPlan, Version: plan.CurrentVersion, ID: planID}}
	for i, draft := range drafts {
		op, err := lower(draft)
		if err != nil {
			return nil, err
		}
		if op, err = packageSource(op, draft, store, "blob-"+strconv.Itoa(i)); err != nil {
			return nil, err
		}
		ops = append(ops, op)
	}
	return ops, nil
}

// requireDrafts refuses the apply when a registered resource has no plan
// draft (e.g. registered through the low-level resource.Register alone): it
// cannot be expressed as a plan op, so applying the rest would silently skip
// it. The wording matches api.Apply's.
func requireDrafts(drafts []resource.PlanDraft, registered []string) error {
	have := make(map[string]bool, len(drafts))
	for _, draft := range drafts {
		have[draft.ID] = true
	}
	var missing []string
	for _, id := range registered {
		if !have[id] {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("Apply: registered resources without plan drafts: %s", strings.Join(missing, ", "))
	}
	return nil
}

// lower converts d to its plan op through the kind's registered handler and
// folds in the draft's elevate and sensitivity flags, as api's draftToOp
// does.
func lower(d resource.PlanDraft) (plan.Op, error) {
	h, ok := plan.HandlerFor(plan.Kind(d.Kind))
	if !ok {
		return plan.Op{}, fmt.Errorf("testapply: draft %q: unknown draft kind %q (is its resource package imported?)", d.ID, d.Kind)
	}
	op, err := h.ToOp(d)
	if err != nil {
		return plan.Op{}, fmt.Errorf("RecordPlan: draft %q: %w", d.ID, err)
	}
	op.Elevate = d.Elevate
	op.Sensitive = op.Sensitive || d.Sensitive
	return op, nil
}

// packageSource attaches d's source data to op: a file source inline as
// content_b64 up to plan.MaxInlineContent and as a blob above it, a glob or
// tree source (sync_dir) always as a blob named name.
func packageSource(op plan.Op, d resource.PlanDraft, store plan.BlobStore, name string) (plan.Op, error) {
	var err error
	switch {
	case d.SourcePath != "":
		var data []byte
		if data, err = os.ReadFile(d.SourcePath); err != nil {
			return op, fmt.Errorf("package file %s: %w", d.SourcePath, err)
		}
		if len(data) <= plan.MaxInlineContent {
			op.ContentB64, op.Blob = base64.StdEncoding.EncodeToString(data), ""
			return op, nil
		}
		op.ContentB64 = ""
		op.Blob, err = store.WriteFile(name, data)
	case d.SourceGlob != "":
		op.Blob, err = store.WriteGlob(name, d.SourceGlob)
	case d.SourceDir != "":
		op.Blob, err = store.WriteTree(name, d.SourceDir)
	}
	return op, err
}

// applyOps runs the whole-plan pre-flight and applies ops with the local
// host's facts. An elevated op is refused: the privilege split lives in
// api.Apply, and running the op in-process would silently drop it.
func applyOps(ops []plan.Op, planDir string) error {
	for _, op := range ops {
		if op.Elevate {
			return fmt.Errorf("testapply: %s is elevated; use api.Apply for privilege-split plans", op.ID)
		}
	}
	if err := plan.ValidateChunks(plan.SplitPrivilegeChunks(ops)); err != nil {
		return fmt.Errorf("Apply: %w", err)
	}
	return plan.Apply(ops, localFacts(), planDir)
}

// localFacts are the host facts plan.Apply evaluates when blocks and
// templates against. Tests register no when blocks, so the profile (which
// api.DetectFacts derives from os-release) is left empty.
func localFacts() plan.Facts {
	host, err := os.Hostname()
	if err != nil {
		host = ""
	}
	return plan.Facts{GOOS: runtime.GOOS, Hostname: host}
}
