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
//  1. refuse before anything runs when plan-record mode is active or a
//     declaration error was reported earlier (internal/declerr), as
//     api.Apply does;
//  2. snapshot the registered plan drafts (resource.RegisteredPlanDrafts) and
//     refuse a registered resource without one, as api.Apply does;
//  3. lower each draft to its plan op through the kind's registered
//     plan.Handler, packaging file, glob and tree sources inline or as blobs
//     in a temporary plan directory;
//  4. run the whole-plan pre-flight api.Apply runs (plan.ValidateChunks over
//     the privilege chunks), so a dangling DependsOn or watch is refused
//     before anything is applied;
//  5. apply the ops with plan.Apply and the local host's facts (GOOS,
//     hostname and profile — see localFacts), which orders them by
//     dependency, evaluates when blocks and renders {{.Gonf.*}} templates,
//     and prints the outcome summary to stderr.
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
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"

	"github.com/snonux/gonf/internal/declerr"
	"github.com/snonux/gonf/internal/hostfacts"
	"github.com/snonux/gonf/internal/runners"
	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
)

// planID is the header ID of every plan this package builds.
const planID = "testapply"

// Apply lowers every registered resource to a plan op and applies the plan
// through plan.Apply, as described in the package comment. It returns nil
// without applying anything when nothing is registered.
//
// Like api.Apply (api/resource.go) it refuses before anything is applied
// when plan-record mode is active (a record session, not an apply, owns the
// registered drafts then) or when a declaration error was reported earlier
// (internal/declerr: DSL misuse such as a duplicate resource registration
// means the registered set is known incomplete). Without these guards a
// resource/<kind> test whose option misuse routed through resource.Refuse
// would see Apply quietly apply the remaining subset and pass, although
// api.Apply/Run/cli.CLI would refuse the whole recipe.
//
// The recording check is plan.Recording() alone, not also
// resource.PlanDraftRecording() as api.Apply's is: the only production path
// that installs a draft recorder (api.RecordPlanTo) always sets both flags
// together for one record session (resource/draft.go), so plan.Recording()
// already catches every real session. Several resource/<kind> tests
// (e.g. TestWithEnvCopiesCallerMap in resource/cmd and resource/pkg) call
// resource.SetPlanDraftRecorder directly, without plan.SetRecording, purely
// to capture the drafts a Present call records for assertions, then still
// apply through this function; folding in PlanDraftRecording() here would
// refuse that legitimate, decoupled pattern.
func Apply() error {
	return ApplyWithRunners(nil)
}

// ApplyWithRunners is Apply with rs injected as the backend runners a
// migrated plan handler uses in place of the real ones, for this one apply
// (task qb2): a resource/<kind> package's own test builds a *runners.Set
// (e.g. &runners.Set{Command: &runners.CommandRunners{Run: fake}}) and
// passes it here instead of installing a process-global
// internal/testseam fake. rs travels down to every handler's ApplyContext
// via ctx (internal/runners.WithSet), scoped to this one call.
func ApplyWithRunners(rs *runners.Set) error {
	if plan.Recording() {
		return fmt.Errorf("testapply: cannot apply while plan recording is active")
	}
	if err := declerr.First(); err != nil {
		return err
	}

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
	ctx := runners.WithSet(context.Background(), rs)
	return applyOps(ctx, ops, planDir)
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
// does. It also runs api's undeclared-kind check (plan.IsKnownKind), except
// for a fixture draft (fixtureKind, deliberately not a declared plan.Kind):
// without this check a handler bug that lowers to an unregistered plan kind
// would pass testapply silently although api.Apply/Run would refuse it.
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
	if op.Op != fixtureKind && !plan.IsKnownKind(op.Op) {
		return op, fmt.Errorf("RecordPlan: draft %q: kind %q lowers to undeclared plan kind %q (missing from plan.AllKinds)",
			d.ID, d.Kind, op.Op)
	}
	return op, nil
}

// packageSource attaches d's source data to op: a file source inline as
// content_b64 up to plan.MaxInlineContent and as a blob above it, a glob or
// tree source (sync_dir) always as a blob named name. The file/sync_dir
// sources moved off resource.PlanDraft's flat fields into resource/file's
// and resource/dir's own Payload types (task w62 Layer 1); this package
// stays kind-neutral (see the package doc: it imports only plan and
// resource, so it never creates a cycle with a resource/<kind> package's
// own tests), so it finds them through the kind-neutral
// resource.SourceFilePayload/SourceDirPayload interfaces instead of
// importing resource/file or resource/dir.
//
// Each type assertion is gated on d.Kind first (task 0e2): w62 Layer 1 left
// them unguarded, consulting whatever concrete type d.Payload held with no
// discriminator, so a future payload that happens to grow a like-named
// SourceFilePath()/SourceDirGlob() for its own, unrelated purpose would
// silently have controller-local file bytes packaged into that OTHER kind's
// op — the same leak api/packager.go's sourceFilePath/syncDirSource guard
// against, and for the identical reason: this package and that one are the
// only two callers of these marker interfaces (see resource/draft.go), so
// both needed the same fix. "ensure_file" joins "file" for the same reason
// api/packager.go's sourceFilePath does: resource/file's planDraft fills
// Payload with file.Payload for both kinds alike.
func packageSource(op plan.Op, d resource.PlanDraft, store plan.BlobStore, name string) (plan.Op, error) {
	var err error
	sourcePath := ""
	if d.Kind == "file" || d.Kind == "ensure_file" {
		if sp, ok := d.Payload.(resource.SourceFilePayload); ok {
			sourcePath = sp.SourceFilePath()
		}
	}
	sourceDir, sourceGlob := "", ""
	if d.Kind == "sync_dir" {
		if sp, ok := d.Payload.(resource.SourceDirPayload); ok {
			sourceDir, sourceGlob = sp.SourceDirGlob()
		}
	}
	switch {
	case sourcePath != "":
		var data []byte
		if data, err = os.ReadFile(sourcePath); err != nil {
			return op, fmt.Errorf("package file %s: %w", sourcePath, err)
		}
		if len(data) <= plan.MaxInlineContent {
			plan.SetFileContentB64(&op, base64.StdEncoding.EncodeToString(data))
			op.Blob = ""
			return op, nil
		}
		plan.SetFileContentB64(&op, "")
		op.Blob, err = store.WriteFile(name, data)
	case sourceGlob != "":
		op.Blob, err = store.WriteGlob(name, sourceGlob)
	case sourceDir != "":
		op.Blob, err = store.WriteTree(name, sourceDir)
	}
	return op, err
}

// applyOps runs the whole-plan pre-flight and applies ops with the local
// host's facts. An elevated op is refused: the privilege split lives in
// api.Apply, and running the op in-process would silently drop it.
func applyOps(ctx context.Context, ops []plan.Op, planDir string) error {
	for _, op := range ops {
		if op.Elevate {
			return fmt.Errorf("testapply: %s is elevated; use api.Apply for privilege-split plans", op.ID)
		}
	}
	if err := plan.ValidateChunks(plan.SplitPrivilegeChunks(ops)); err != nil {
		return fmt.Errorf("Apply: %w", err)
	}
	return plan.ApplyWithContext(ctx, ops, localFacts(), planDir)
}

// localFacts are the host facts plan.Apply evaluates when blocks and, for a
// templated File/sync_dir entry, {{.Gonf.Profile}} against
// (EnsureWithPlanFacts, resource/file/planwire.go). Profile comes from
// internal/hostfacts, the same detection api.DetectFacts and resource/file's
// template facts use, so a template rendered through testapply.Apply matches
// what the real apply reports on this host. It ignores
// api.SetProfileOverride, which only a live CLI process sets.
func localFacts() plan.Facts {
	host, err := os.Hostname()
	if err != nil {
		host = ""
	}
	return plan.Facts{GOOS: runtime.GOOS, Hostname: host, Profile: hostfacts.Profile(host, runtime.GOOS)}
}
