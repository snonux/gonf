package api

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
)

// recordingElevate is set while recording a Privileged() task body.
var recordingElevate bool

// recordedDraftIDs holds the plan draft IDs emitted during the current task
// body. The recorder fills it; recordTaskBodies resets it per task and fails
// the record when a registered resource produced no draft (such resources
// would be silently skipped by plan apply).
var recordedDraftIDs = map[string]bool{}

// recordingStack lists the task bodies currently being recorded, outermost
// first. Re-entering a task whose body is still on the stack is a recursion
// cycle (a task that Runs itself, directly or through other tasks). The same
// task appearing again in a later, disjoint branch (diamond includes) is not
// a cycle: by then its earlier body has left the stack, so repeats across
// branches stay legal and only true cycles fail the record.
var recordingStack []string

// recordingCycleErr holds a detected task recursion cycle. Task bodies cannot
// return errors, so the nested recordTaskBodies that detected the cycle
// stashes it here; every enclosing body fails its record after its fn
// returns. It is cleared at the start of each RecordPlanTo session.
var recordingCycleErr error

// recordingPackErr holds the current recording session's packaging error
// (draft-lowering or blob-pack failure). The recorder callback sets it; every
// task body — including bodies recorded through nested Run calls — checks it
// after running, so an outer session's pack failure fails enclosing nested
// bodies with the REAL error instead of a misleading secondary
// checkUnrecordedDrafts one. Recording is single-goroutine (fleet records
// centrally before fan-out), so a plain package-level value is safe.
var recordingPackErr error

// recordingBodyErr holds a task-body failure stashed by Aggregate (whose
// Task fn cannot return errors): a child Run error, or a pattern that
// matched no tasks. Like the cycle stash, every enclosing body fails its
// record after its fn returns, and the top-level RecordPlanTo returns it —
// so Run's deferred temp-dir cleanup runs and embedded callers get an error
// instead of a process exit. Cleared at the start of each RecordPlanTo
// session.
var recordingBodyErr error

// RecordPlan runs the named tasks in plan-record mode: resource registration
// emits plan.Op lines instead of applying. Tasks are looked up as candidates
// (not Activate-filtered) so When* recipes become when_begin/when_end rather
// than being resolved on the controller. InstallFile sources are packaged as
// content_b64 (or blob sidecars when large); SyncDir trees are copied under
// planDir/blobs/. planDir may be empty when no SyncDir or large-file packaging
// is needed.
//
// Nested Run calls while recording append into the same plan (used by Aggregate).
func RecordPlan(planID, planDir string, taskNames ...string) ([]plan.Op, error) {
	var store plan.BlobStore
	if planDir != "" {
		if err := os.MkdirAll(planDir, 0o700); err != nil {
			return nil, fmt.Errorf("RecordPlan: plan dir: %w", err)
		}
		store = plan.NewStore(planDir)
	}
	return RecordPlanTo(planID, store, taskNames...)
}

// RecordPlanTo is like RecordPlan but packages blobs into store (disk or memory).
// Pass a nil store only when tasks need no blob packaging.
func RecordPlanTo(planID string, store plan.BlobStore, taskNames ...string) ([]plan.Op, error) {
	if planID == "" {
		return nil, fmt.Errorf("RecordPlan: plan id must not be empty")
	}
	if len(taskNames) == 0 {
		return nil, fmt.Errorf("RecordPlan: no tasks specified")
	}

	plan.ResetRecord()
	plan.SetRecording(true)
	recordingCycleErr = nil
	recordingBodyErr = nil
	// Defensive: a task body panicking during recording would leak a stale
	// stack entry (pop is skipped); go test recovers per-test panics and
	// keeps running, so reset here to keep later sessions truthful.
	recordingStack = nil
	recordingPackErr = nil
	resource.SetPlanDraftRecorder(func(d resource.PlanDraft) {
		if recordingPackErr != nil {
			return
		}
		if d.ID != "" {
			recordedDraftIDs[d.ID] = true
		}
		op, err := packageDraft(d, store)
		if err != nil {
			recordingPackErr = err
			return
		}
		plan.Record(op)
	})
	defer func() {
		resource.SetPlanDraftRecorder(nil)
		plan.SetRecording(false)
	}()

	if err := recordTaskBodies(taskNames); err != nil {
		return nil, err
	}
	if recordingPackErr != nil {
		return nil, recordingPackErr
	}

	ops := plan.FinishRecord(planID)
	plan.ResetRecord()
	return ops, nil
}

// recordTaskBodies appends ops for taskNames into the current plan session.
// Packaging failures from the session's draft recorder are shared through
// recordingPackErr, so nested Run bodies see the real error too.
func recordTaskBodies(taskNames []string) error {
	for _, name := range taskNames {
		if err := checkRecordingCycle(name); err != nil {
			// Task bodies cannot return errors; stash the cycle so every
			// enclosing body fails its record too.
			recordingCycleErr = err
			return err
		}
		recordingStack = append(recordingStack, name)
		err := recordSingleTaskBody(name)
		recordingStack = recordingStack[:len(recordingStack)-1]
		if err != nil {
			return err
		}
	}
	return nil
}

// recordSingleTaskBody records one task body into the current plan session.
func recordSingleTaskBody(name string) error {
	c, ok := findCandidate(name)
	if !ok {
		return fmt.Errorf("unknown task %q", name)
	}

	wrapWhen, err := planWhenForCandidate(c)
	if err != nil {
		return err
	}

	prevElevate := recordingElevate
	recordingElevate = c.privileged
	if len(wrapWhen) > 0 {
		plan.Record(plan.Op{
			Op:      plan.KindWhenBegin,
			ID:      "when." + name,
			All:     wrapWhen,
			Elevate: recordingElevate,
		})
	}

	resource.ResetRepository()
	resetRecordedDrafts()
	c.fn()
	if recordingCycleErr != nil {
		recordingElevate = prevElevate
		// Keep the stash set: enclosing bodies fail with the same cycle.
		return recordingCycleErr
	}
	if recordingBodyErr != nil {
		recordingElevate = prevElevate
		// Keep the stash set: enclosing bodies fail with the same error.
		return recordingBodyErr
	}
	if recordingPackErr != nil {
		recordingElevate = prevElevate
		// Keep the stash set: enclosing bodies fail with the same error.
		return recordingPackErr
	}
	if err := checkUnrecordedDrafts(c.name); err != nil {
		recordingElevate = prevElevate
		return err
	}

	if len(wrapWhen) > 0 {
		plan.Record(plan.Op{Op: plan.KindWhenEnd, Elevate: recordingElevate})
	}
	recordingElevate = prevElevate
	return nil
}

// checkRecordingCycle fails when name is already on the active recording
// stack: the task's body re-entered itself, directly or through other task
// bodies. The error names the cycle chain, e.g. a -> b -> c -> a.
func checkRecordingCycle(name string) error {
	for i, onStack := range recordingStack {
		if onStack != name {
			continue
		}
		chain := append([]string{}, recordingStack[i:]...)
		chain = append(chain, name)
		return fmt.Errorf("task recursion cycle detected: %s",
			strings.Join(chain, " -> "))
	}
	return nil
}

// resetRecordedDrafts clears the per-task-body draft ID set.
func resetRecordedDrafts() {
	for id := range recordedDraftIDs {
		delete(recordedDraftIDs, id)
	}
}

// stashBodyError records a task-body failure for the current recording
// session. Task bodies cannot return errors, so bodies that fail (Aggregate
// stashes its child Run error there) record it; every enclosing body fails
// its record after its fn returns. The first error wins, and later stashes
// wrap it so the aggregate include chain stays visible to the operator.
func stashBodyError(err error) {
	if recordingBodyErr == nil {
		recordingBodyErr = err
		return
	}
	recordingBodyErr = fmt.Errorf("aggregate %s: %w", currentRecordingName(), recordingBodyErr)
}

// currentRecordingName returns the innermost task body being recorded, for
// error context when multiple bodies stash failures.
func currentRecordingName() string {
	if len(recordingStack) == 0 {
		return "?"
	}
	return recordingStack[len(recordingStack)-1]
}

// checkUnrecordedDrafts returns an error when a registered resource did not
// produce a plan draft: plan apply only interprets recorded ops, so such a
// resource would be silently skipped. Failing the record keeps future resource
// kinds from regressing the same way cron and service once did.
func checkUnrecordedDrafts(taskName string) error {
	var missing []string
	for _, id := range resource.RegisteredIDs() {
		if !recordedDraftIDs[id] {
			missing = append(missing, id)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("RecordPlan: task %q: registered resources without plan draft (they would be silently skipped by apply): %s",
		taskName, strings.Join(missing, ", "))
}

// ApplyPlan applies ops using DetectFacts(). planDir is the blob sidecar root.
func ApplyPlan(ops []plan.Op, planDir string) error {
	f := DetectFacts()
	return plan.Apply(ops, plan.Facts{
		GOOS:     f.GOOS,
		Profile:  f.Profile,
		Hostname: f.Hostname,
	}, planDir)
}

// planWhenForCandidate returns serializable when predicates, or nil when the
// task has no When. Opaque When predicates must still pass on the controller.
func planWhenForCandidate(c taskCandidate) ([]plan.Predicate, error) {
	if len(c.when) == 0 {
		return nil, nil
	}
	if !c.opaqueWhen && len(c.planWhen) > 0 {
		out := make([]plan.Predicate, len(c.planWhen))
		copy(out, c.planWhen)
		return out, nil
	}
	if !whenPasses(c.when, DetectFacts()) {
		return nil, fmt.Errorf("RecordPlan: task %q When predicates fail on controller and are not serializable", c.name)
	}
	return nil, nil
}

func packageDraft(d resource.PlanDraft, store plan.BlobStore) (plan.Op, error) {
	op, err := draftToOp(d)
	if err != nil {
		return plan.Op{}, err
	}
	switch {
	case d.SourcePath != "":
		data, err := os.ReadFile(d.SourcePath)
		if err != nil {
			return op, fmt.Errorf("package file %s: %w", d.SourcePath, err)
		}
		if len(data) > plan.MaxInlineContent {
			if store == nil {
				return op, fmt.Errorf("package file %s: exceeds inline limit and no plan dir for blobs", d.SourcePath)
			}
			ref, err := store.WriteFile(blobName(d), data)
			if err != nil {
				return op, err
			}
			op.Blob = ref
			op.ContentB64 = ""
		} else {
			op.ContentB64 = base64.StdEncoding.EncodeToString(data)
			op.Blob = ""
		}
	case d.SourceGlob != "":
		if store == nil {
			return op, fmt.Errorf("package sync_dir %s: plan dir required for blob packaging", d.SourceGlob)
		}
		ref, err := store.WriteGlob(blobName(d), d.SourceGlob)
		if err != nil {
			return op, err
		}
		op.Blob = ref
	case d.SourceDir != "":
		if store == nil {
			return op, fmt.Errorf("package sync_dir %s: plan dir required for blob packaging", d.SourceDir)
		}
		ref, err := store.WriteTree(blobName(d), d.SourceDir)
		if err != nil {
			return op, err
		}
		op.Blob = ref
	}
	return op, nil
}

func blobName(d resource.PlanDraft) string {
	if base := filepath.Base(d.Path); base != "" && base != "." && base != string(filepath.Separator) {
		return base
	}
	if d.ID != "" {
		return d.ID
	}
	if d.SourcePath != "" {
		return filepath.Base(d.SourcePath)
	}
	if d.SourceDir != "" {
		return filepath.Base(d.SourceDir)
	}
	if d.SourceGlob != "" {
		dir := filepath.Dir(d.SourceGlob)
		if base := filepath.Base(dir); base != "" && base != "." {
			return base
		}
		return "glob"
	}
	return "blob"
}

// draftToOp lowers a resource draft to a plan op line. Every draft Kind must
// map through an explicit switch case: an unmapped kind is a programming
// error (typo, or a new resource kind missing its draftToOp case) and fails
// the record loudly instead of silently forwarding an unknown op to the wire,
// where it would only blow up at remote apply time. See docs/plan.md,
// "Adding a resource kind" for the full checklist.
func draftToOp(d resource.PlanDraft) (plan.Op, error) {
	op := plan.Op{
		ID:         d.ID,
		Path:       d.Path,
		Symlink:    d.Symlink,
		Hardlink:   d.Hardlink,
		Target:     d.Target,
		Mode:       d.Mode,
		FileMode:   d.FileMode,
		Owner:      d.Owner,
		Group:      d.Group,
		ContentB64: d.ContentB64,
		Blob:       d.Blob,
		Prune:      d.Prune,
		Absent:     d.Absent,
		AddLine:    d.AddLine,
		RemoveLine: d.RemoveLine,
		Name:       d.Name,
		Bin:        d.Bin,
		Args:       d.Args,
		Dir:        d.Dir,
		Env:        d.Env,
		Creates:    d.Creates,
		Unless:     draftGuard(d.Unless),
		OnlyIf:     draftGuard(d.OnlyIf),
		User:       d.User,
		CronUser:   d.CronUser,
		Command:    d.Command,
		Schedule:   d.Schedule,
		CronEnv:    d.CronEnv,
		Restart:    d.Restart,
		Reload:     d.Reload,
		EnableOnly: d.EnableOnly,
		IfChanged:  d.IfChanged,
		Watch:      d.Watch,
		Deps:       d.Deps,
		Elevate:    d.Elevate || recordingElevate,
	}
	switch d.Kind {
	case "file":
		op.Op = plan.KindFile
	case "dir":
		op.Op = plan.KindDir
	case "sync_dir":
		op.Op = plan.KindSyncDir
	case "link":
		op.Op = plan.KindLink
	case "package":
		op.Op = plan.KindPackage
	case "command":
		op.Op = plan.KindCommand
	case "ensure_dir":
		op.Op = plan.KindEnsureDir
	case "link_if_exists":
		op.Op = plan.KindLinkIfExists
	case "timer":
		op.Op = plan.KindTimer
	case "daemon_reload":
		op.Op = plan.KindDaemonReload
	case "cron":
		op.Op = plan.KindCron
	case "service":
		op.Op = plan.KindService
	default:
		return op, fmt.Errorf("RecordPlan: draft %q: unknown draft kind %q (no draftToOp case; see docs/plan.md kind checklist)",
			d.ID, d.Kind)
	}
	if !plan.IsKnownKind(op.Op) {
		return op, fmt.Errorf("RecordPlan: draft %q: kind %q lowers to undeclared plan kind %q (missing from plan.AllKinds)",
			d.ID, d.Kind, op.Op)
	}
	return op, nil
}

func draftGuard(g *resource.PlanGuardDraft) *plan.Guard {
	if g == nil {
		return nil
	}
	return &plan.Guard{
		Bin:          g.Bin,
		Args:         g.Args,
		ExpectStdout: g.ExpectStdout,
		ExpectExit:   g.ExpectExit,
	}
}
