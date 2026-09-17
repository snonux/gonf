package api

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
)

// recordingSession carries the mutable state of one plan recording session.
// It deliberately replaces six loose package globals with one struct so the
// fields stay together and reset in a single place (reset). Recording is
// single-goroutine (fleet records centrally before fan-out), so no mutex
// guards it. The other two members of the record-mode trio — plan
// recording and the resource draft recorder — are set together with this
// session by RecordPlanTo; see plan/record.go and resource/draft.go.
type recordingSession struct {
	// recordingElevate is set while recording a Privileged() task body.
	recordingElevate bool

	// recordedDraftIDs holds the plan draft IDs emitted during the current
	// task body. The recorder fills it; recordTaskBodies resets it per task
	// and fails the record when a registered resource produced no draft
	// (such resources would be silently skipped by plan apply).
	recordedDraftIDs map[string]bool

	// recordingStack lists the task bodies currently being recorded,
	// outermost first. Re-entering a task whose body is still on the stack
	// is a recursion cycle (a task that Runs itself, directly or through
	// other tasks). The same task appearing again in a later, disjoint
	// branch (diamond includes) is not a cycle: by then its earlier body
	// has left the stack, so repeats across branches stay legal and only
	// true cycles fail the record.
	recordingStack []string

	// recordingCycleErr holds a detected task recursion cycle. Task bodies
	// cannot return errors, so the nested recordTaskBodies that detected
	// the cycle stashes it here; every enclosing body fails its record
	// after its fn returns. It is cleared at the start of each
	// RecordPlanTo session.
	recordingCycleErr error

	// recordingPackErr holds the current recording session's packaging
	// error (draft-lowering or blob-pack failure). The recorder callback
	// sets it; every task body — including bodies recorded through nested
	// Run calls — checks it after running, so an outer session's pack
	// failure fails enclosing nested bodies with the REAL error instead of
	// a misleading secondary checkUnrecordedDrafts one. Recording is
	// single-goroutine (fleet records centrally before fan-out), so a plain
	// package-level value is safe.
	recordingPackErr error

	// recordingBodyErr holds a task-body failure stashed by Aggregate
	// (whose Task fn cannot return errors): a child Run error, or a pattern
	// that matched no tasks. Like the cycle stash, every enclosing body
	// fails its record after its fn returns, and the top-level
	// RecordPlanTo returns it — so Run's deferred temp-dir cleanup runs and
	// embedded callers get an error instead of a process exit. Cleared at
	// the start of each RecordPlanTo session.
	recordingBodyErr error

	// recordedBlobRefs maps every blob ref written during the current
	// recording session to the resource identity (blobIdentityKey) that
	// produced it. blobName folds a hash of that same identity into the
	// ref so two different resources can never generate the same ref (the
	// bug this map guards against: two SyncDir/File drafts whose
	// destinations merely share a basename used to collide on
	// e.g. "blobs/conf.d", so the second write silently clobbered the
	// first). This map is defense in depth on top of that: it catches an
	// unexpected ref collision (a hash truncation clash, or a future
	// blobName regression) loudly instead of letting a second write
	// silently overwrite a first one in the blob store. A resource
	// re-recorded with the *same* identity (e.g. a diamond-included task
	// body running twice) legitimately reuses its own ref, so only a ref
	// reused by a *different* identity is an error.
	recordedBlobRefs map[string]string

	// recordedOpaqueOnlyTasks lists task names recorded during the current
	// session whose When is opaque with NO serializable guard at all (a bare
	// When(func), or When(func) with no WhenLinux/WhenProfile/
	// WhenHostnameContains alongside it). For such a task, planWhenForCandidate
	// emits no when_begin: the condition only ever ran on the controller.
	// That is fine for local Run/gonf-plan (apply happens on the same host
	// that just evaluated it), but PushTo/PushClusterRun/PushFleetRun check
	// this list and refuse to ship the plan — shipping it would silently
	// drop the guard and apply the task unconditionally on the destination.
	recordedOpaqueOnlyTasks []string
}

// recSession is the process-wide plan recording session. Single-goroutine
// by the DSL invariant above.
var recSession recordingSession

// reset clears all session state — errors, stack, draft IDs, elevate — so
// nothing stale leaks between recording sessions or into tests
// (api.ResetForTest uses it the same way).
func (s *recordingSession) reset() {
	s.recordingElevate = false
	s.recordedDraftIDs = map[string]bool{}
	s.recordingStack = nil
	s.recordingCycleErr = nil
	s.recordingPackErr = nil
	s.recordingBodyErr = nil
	s.recordedBlobRefs = map[string]string{}
	s.recordedOpaqueOnlyTasks = nil
}

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
	// Defensive: a task body panicking during recording would leak a stale
	// stack entry (pop is skipped); go test recovers per-test panics and
	// keeps running, so reset here to keep later sessions truthful.
	recSession.reset()
	resource.SetPlanDraftRecorder(func(d resource.PlanDraft) {
		if recSession.recordingPackErr != nil {
			return
		}
		if d.ID != "" {
			recSession.recordedDraftIDs[d.ID] = true
		}
		op, err := packageDraft(d, store)
		if err != nil {
			recSession.recordingPackErr = err
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
	if recSession.recordingPackErr != nil {
		return nil, recSession.recordingPackErr
	}

	ops := plan.FinishRecord(planID)
	plan.ResetRecord()
	return ops, nil
}

// RefuseOpaqueOnlyPush errors when the RecordPlanTo call that just returned
// ops recorded one or more tasks whose When is opaque with no serializable
// guard at all (see recordedOpaqueOnlyTasks). PushTo, PushClusterRun, and
// PushFleetRun call this right after RecordPlanTo and before streaming
// anything over SSH, so an unsafe plan is refused before any destination is
// touched — consistent with the rest of RecordPlan's record-before-mutate
// error contract (docs/plan.md "Error handling contract"). action names the
// caller for the error message (e.g. "push", "cluster \"web\"").
//
// Local Run / gonf plan do not call this: they apply (or hand the plan to
// something that applies) on the very host that just evaluated the opaque
// predicate, so there is no guard to lose in transit.
func RefuseOpaqueOnlyPush(action string) error {
	if len(recSession.recordedOpaqueOnlyTasks) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(recSession.recordedOpaqueOnlyTasks))
	names := make([]string, 0, len(recSession.recordedOpaqueOnlyTasks))
	for _, n := range recSession.recordedOpaqueOnlyTasks {
		if _, ok := seen[n]; ok {
			continue
		}
		seen[n] = struct{}{}
		names = append(names, n)
	}
	sort.Strings(names)
	return fmt.Errorf(
		"%s: task(s) %s use When(func) with no serializable guard (WhenLinux/WhenProfile/WhenHostnameContains); "+
			"shipping this plan would drop the guard entirely and apply the task unconditionally on the destination — refusing to push",
		action, strings.Join(names, ", "))
}

// recordTaskBodies appends ops for taskNames into the current plan session.
// Packaging failures from the session's draft recorder are shared through
// recordingPackErr, so nested Run bodies see the real error too.
func recordTaskBodies(taskNames []string) error {
	for _, name := range taskNames {
		if err := checkRecordingCycle(name); err != nil {
			// Task bodies cannot return errors; stash the cycle so every
			// enclosing body fails its record too.
			recSession.recordingCycleErr = err
			return err
		}
		recSession.recordingStack = append(recSession.recordingStack, name)
		err := recordSingleTaskBody(name)
		recSession.recordingStack = recSession.recordingStack[:len(recSession.recordingStack)-1]
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
	if c.opaqueWhen && len(wrapWhen) == 0 {
		// Passed the controller-side opaque filter (planWhenForCandidate
		// would have errored otherwise) but has no serializable guard to
		// ship: see recordedOpaqueOnlyTasks.
		recSession.recordedOpaqueOnlyTasks = append(recSession.recordedOpaqueOnlyTasks, c.name)
	}

	prevElevate := recSession.recordingElevate
	recSession.recordingElevate = c.privileged
	if len(wrapWhen) > 0 {
		plan.Record(plan.Op{
			Op:      plan.KindWhenBegin,
			ID:      "when." + name,
			All:     wrapWhen,
			Elevate: recSession.recordingElevate,
		})
	}

	resource.ResetRepository()
	resetRecordedDrafts()
	c.fn()
	if recSession.recordingCycleErr != nil {
		recSession.recordingElevate = prevElevate
		// Keep the stash set: enclosing bodies fail with the same cycle.
		return recSession.recordingCycleErr
	}
	if recSession.recordingBodyErr != nil {
		recSession.recordingElevate = prevElevate
		// Keep the stash set: enclosing bodies fail with the same error.
		return recSession.recordingBodyErr
	}
	if recSession.recordingPackErr != nil {
		recSession.recordingElevate = prevElevate
		// Keep the stash set: enclosing bodies fail with the same error.
		return recSession.recordingPackErr
	}
	if err := checkUnrecordedDrafts(c.name); err != nil {
		recSession.recordingElevate = prevElevate
		return err
	}

	if len(wrapWhen) > 0 {
		plan.Record(plan.Op{Op: plan.KindWhenEnd, Elevate: recSession.recordingElevate})
	}
	recSession.recordingElevate = prevElevate
	return nil
}

// checkRecordingCycle fails when name is already on the active recording
// stack: the task's body re-entered itself, directly or through other task
// bodies. The error names the cycle chain, e.g. a -> b -> c -> a.
func checkRecordingCycle(name string) error {
	for i, onStack := range recSession.recordingStack {
		if onStack != name {
			continue
		}
		chain := append([]string{}, recSession.recordingStack[i:]...)
		chain = append(chain, name)
		return fmt.Errorf("task recursion cycle detected: %s",
			strings.Join(chain, " -> "))
	}
	return nil
}

// resetRecordedDrafts clears the per-task-body draft ID set.
func resetRecordedDrafts() {
	for id := range recSession.recordedDraftIDs {
		delete(recSession.recordedDraftIDs, id)
	}
}

// stashBodyError records a task-body failure for the current recording
// session. Task bodies cannot return errors, so bodies that fail (Aggregate
// stashes its child Run error there) record it; every enclosing body fails
// its record after its fn returns. The first error wins, and later stashes
// wrap it so the aggregate include chain stays visible to the operator.
func stashBodyError(err error) {
	if recSession.recordingBodyErr == nil {
		recSession.recordingBodyErr = err
		return
	}
	recSession.recordingBodyErr = fmt.Errorf("aggregate %s: %w", currentRecordingName(), recSession.recordingBodyErr)
}

// currentRecordingName returns the innermost task body being recorded, for
// error context when multiple bodies stash failures.
func currentRecordingName() string {
	if len(recSession.recordingStack) == 0 {
		return "?"
	}
	return recSession.recordingStack[len(recSession.recordingStack)-1]
}

// checkUnrecordedDrafts returns an error when a registered resource did not
// produce a plan draft: plan apply only interprets recorded ops, so such a
// resource would be silently skipped. Failing the record keeps future resource
// kinds from regressing the same way cron and service once did.
func checkUnrecordedDrafts(taskName string) error {
	var missing []string
	for _, id := range resource.RegisteredIDs() {
		if !recSession.recordedDraftIDs[id] {
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

// planWhenForCandidate returns the serializable when predicates to record as
// a when_begin guard, or nil when the task has no When predicates at all
// (or none of them are serializable).
//
// Any opaque (non-serializable, e.g. a custom When(func)) predicate in
// c.when is evaluated here as an extra controller-side filter: it gates
// whether recording proceeds at all (an error when it fails), but it never
// takes the place of the serializable guards — those are always emitted
// when present, even alongside an opaque predicate. This matters because
// c.when accumulates every When call regardless of serializability, so a
// task built with WhenLinux() + When(fn) must still ship its goos guard.
func planWhenForCandidate(c taskCandidate) ([]plan.Predicate, error) {
	if len(c.when) == 0 {
		return nil, nil
	}
	if c.opaqueWhen && !whenPasses(c.when, DetectFacts()) {
		return nil, fmt.Errorf("RecordPlan: task %q When predicates fail on controller and are not serializable", c.name)
	}
	if len(c.planWhen) == 0 {
		return nil, nil
	}
	out := make([]plan.Predicate, len(c.planWhen))
	copy(out, c.planWhen)
	return out, nil
}

func packageDraft(d resource.PlanDraft, store plan.BlobStore) (plan.Op, error) {
	op, err := draftToOp(d)
	if err != nil {
		return plan.Op{}, err
	}
	name := blobName(d)
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
			if err := guardBlobRef(name, d); err != nil {
				return op, err
			}
			ref, err := store.WriteFile(name, data)
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
		if err := guardBlobRef(name, d); err != nil {
			return op, err
		}
		ref, err := store.WriteGlob(name, d.SourceGlob)
		if err != nil {
			return op, err
		}
		op.Blob = ref
	case d.SourceDir != "":
		if store == nil {
			return op, fmt.Errorf("package sync_dir %s: plan dir required for blob packaging", d.SourceDir)
		}
		if err := guardBlobRef(name, d); err != nil {
			return op, err
		}
		ref, err := store.WriteTree(name, d.SourceDir)
		if err != nil {
			return op, err
		}
		op.Blob = ref
	}
	return op, nil
}

// blobName returns the blob ref name for d: a human-readable basename (the
// destination's last path segment, when there is one) followed by a short
// hash of blobIdentityKey(d). The hash is what actually guarantees
// uniqueness — two drafts whose destinations merely share a basename (e.g.
// Dir(/x/conf.d, WithSource(a)) and Dir(/y/conf.d, WithSource(b)), or two
// >512KiB Files with equal basenames) previously both packaged to
// "blobs/conf.d", so the second store.Write* call silently overwrote the
// first one's content; guardBlobRef below is the defense-in-depth backstop
// in case a future change reintroduces a real collision anyway.
func blobName(d resource.PlanDraft) string {
	return blobBaseName(d) + "-" + shortHash(blobIdentityKey(d))
}

// blobBaseName returns the human-readable part of blobName, unchanged from
// the original (pre-hash) naming scheme so blob directory listings stay
// legible.
func blobBaseName(d resource.PlanDraft) string {
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

// blobIdentityKey returns a string that uniquely identifies the resource
// occurrence being packaged, so blobName's hash suffix cannot collide
// between two different resources. d.ID (the registered "Type[Name]" id,
// e.g. "Directory[/x/conf.d]") already carries the full destination path,
// so it alone distinguishes any two drafts with different destinations.
// The fallback is defense-in-depth for a PlanDraft with no ID: every
// real draft-construction site sets ID today, so this path is currently
// unreachable, but if it ever fires it combines every source/destination
// field so two ID-less drafts still get different keys whenever any of
// their paths differ.
func blobIdentityKey(d resource.PlanDraft) string {
	if d.ID != "" {
		return d.ID
	}
	return strings.Join([]string{d.Path, d.SourcePath, d.SourceDir, d.SourceGlob}, "\x00")
}

// shortHash returns a short, fixed-width hex fingerprint of key, used to
// disambiguate blob refs that would otherwise share a human-readable
// basename. It is not required to be stable across gonf versions or plan
// runs — docs/plan.md already treats blob paths as ephemeral, record-time
// artifacts, e.g. the source_dir field exists precisely so destination apply
// never depends on the blob path staying the same between runs.
func shortHash(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])[:8]
}

// guardBlobRef predicts the blob ref that store.Write{File,Tree,Glob} will
// produce for name and fails loudly if a *different* resource already
// claimed that exact ref earlier in this recording session. blobName's hash
// suffix should already make that impossible; this is defense in depth so a
// regression here fails RecordPlan instead of silently corrupting a blob
// (the data-loss failure mode this whole fix exists to close). A resource
// recorded twice with the same identity (e.g. a diamond-included task body
// running again in a disjoint branch) legitimately reuses its own ref, so
// that case is not an error.
func guardBlobRef(name string, d resource.PlanDraft) error {
	ref, err := plan.BlobRefFor(name)
	if err != nil {
		return err
	}
	identity := blobIdentityKey(d)
	if prior, ok := recSession.recordedBlobRefs[ref]; ok {
		if prior == identity {
			return nil
		}
		return fmt.Errorf("RecordPlan: blob ref %q collision: already packaged for %q, now requested for %q (this should be impossible after blobName hashing; please report)",
			ref, prior, identity)
	}
	recSession.recordedBlobRefs[ref] = identity
	return nil
}

// draftToOp lowers a resource draft to a plan op line.
//
// A kind whose resource package has registered a plan.Handler (see
// plan/handler.go) delegates entirely to that handler's ToOp: the resource
// package owns its own wire form and this function only folds in the
// recording session's Elevate flag. Every other draft Kind still goes
// through the explicit switch case below: an unmapped kind is a programming
// error (typo, or a new resource kind missing both a Handler registration
// and a draftToOp case) and fails the record loudly instead of silently
// forwarding an unknown op to the wire, where it would only blow up at
// remote apply time. See docs/plan.md, "Adding a resource kind" for the full
// checklist.
func draftToOp(d resource.PlanDraft) (plan.Op, error) {
	if h, ok := plan.HandlerFor(plan.Kind(d.Kind)); ok {
		op, err := h.ToOp(d)
		if err != nil {
			return plan.Op{}, err
		}
		op.Elevate = d.Elevate || recSession.recordingElevate
		if !plan.IsKnownKind(op.Op) {
			return op, fmt.Errorf("RecordPlan: draft %q: kind %q lowers to undeclared plan kind %q (missing from plan.AllKinds)",
				d.ID, d.Kind, op.Op)
		}
		return op, nil
	}

	op := plan.Op{
		ID:                 d.ID,
		Path:               d.Path,
		Symlink:            d.Symlink,
		Hardlink:           d.Hardlink,
		Target:             d.Target,
		Mode:               d.Mode,
		FileMode:           d.FileMode,
		Owner:              d.Owner,
		Group:              d.Group,
		ContentB64:         d.ContentB64,
		Blob:               d.Blob,
		HasContent:         d.HasContent,
		Template:           d.Template,
		TemplateParam:      d.TemplateParam,
		SourceDir:          d.SourceDir,
		Prune:              d.Prune,
		Absent:             d.Absent,
		Latest:             d.Latest,
		AddLine:            d.AddLine,
		RemoveLine:         d.RemoveLine,
		Name:               d.Name,
		Bin:                d.Bin,
		Args:               d.Args,
		Dir:                d.Dir,
		Env:                d.Env,
		Creates:            d.Creates,
		Unless:             draftGuard(d.Unless),
		OnlyIf:             draftGuard(d.OnlyIf),
		User:               d.User,
		CronUser:           d.CronUser,
		Command:            d.Command,
		Schedule:           d.Schedule,
		CronEnv:            d.CronEnv,
		OnCalendar:         d.OnCalendar,
		OnBootSec:          d.OnBootSec,
		Persistent:         d.Persistent,
		Description:        d.Description,
		ServiceDescription: d.ServiceDescription,
		After:              d.After,
		Wants:              d.Wants,
		Restart:            d.Restart,
		Reload:             d.Reload,
		EnableOnly:         d.EnableOnly,
		IfChanged:          d.IfChanged,
		Watch:              d.Watch,
		Deps:               d.Deps,
		Elevate:            d.Elevate || recSession.recordingElevate,
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
	case "systemd_timer":
		op.Op = plan.KindSystemdTimer
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
