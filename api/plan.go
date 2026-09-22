package api

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
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
// guards it. The other members of the record-mode set — plan recording,
// the resource draft recorder and the resource draft amender — are set
// together with this session by RecordPlanTo and cleared when it returns;
// see plan/record.go and resource/draft.go.
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

	// recordingBodyErr holds a task-body failure stashed because a Task fn
	// cannot return errors: an aggregate's child failure, empty member set
	// or broken alias (Aggregate/AggregateTasks), a failed nested Run from
	// any task body (propagateNestedRunError), a secret lookup failure
	// (stashSecretError), or a ForHosts misuse or missing/mistyped host
	// value (failForHosts). Like the cycle stash, every enclosing body
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

	// aggregateSeen holds the task names (alias targets, never alias names)
	// already recorded by the aggregate tree currently being recorded, so a
	// member reachable twice — through an alias, or through two nested
	// aggregates — records once, at its first position. It is nil outside an
	// aggregate. The outermost aggregate creates it and clears it when done;
	// a non-aggregate task body recorded inside the tree runs with its own
	// nil scope (enterAggregateScope), because such a body may add a When or
	// Privileged envelope that the outer occurrence does not share. Aggregates
	// and aliases have no options of their own, so everything inside one
	// scope is recorded under the same guards and deduplication is exact.
	aggregateSeen map[string]bool
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
	s.aggregateSeen = nil
}

// RecordPlan runs the named tasks in plan-record mode: resource registration
// emits plan.Op lines instead of applying. Tasks are looked up as candidates
// (not Activate-filtered) so When* recipes become when_begin/when_end rather
// than being resolved on the controller. InstallFile sources are packaged as
// content_b64 (or blob sidecars when large); SyncDir trees are copied under
// planDir/blobs/. planDir may be empty when no SyncDir or large-file packaging
// is needed.
//
// What RecordPlan guarantees about planDir. Blob names are deterministic
// (basename plus a hash of the resource ID), so a refused record that wrote
// straight into a directory holding an earlier good plan would overwrite that
// plan's blobs — the older plan.jsonl would then apply content it was never
// recorded with — and would leave stray blobs (or create an empty directory)
// even though no plan came out. RecordPlan therefore records into a private
// staging directory (stageBlobs) and copies the referenced blobs into planDir
// only after RecordPlanTo, including its dependency and change-gate
// pre-flight, returned a plan:
//
//   - Every error that arises while recording or validating (task-body,
//     cycle, packaging and pre-flight refusals) leaves planDir exactly as it
//     was, and an absent planDir is not created. So does the refusal of an
//     unusable planDir (a symlink or a symlinked ancestor, a file, another
//     user's directory, one that others or a shared group can write, one the
//     caller cannot write to, no writable ancestor): a best-effort pre-check
//     (checkPlanDirUsable) catches those before any task body runs, and since
//     the path can change afterwards the commit re-checks the same things
//     (plan.OpenSecureStore, then the writability check on the directory it
//     holds open) before the first blob is written, after the task bodies ran. An unsafe existing planDir/blobs (a
//     symlink, or one others or a shared group can write) is only ever caught
//     at commit time, because a plan without blobs never touches it; it too
//     is refused before any blob is written.
//   - An I/O failure while COMMITTING the blobs (full disk, permissions, a
//     blob path that cannot be replaced) is reported but is not atomic: some
//     blobs may already be copied, so a partially updated blob store is
//     possible. A missing planDir is created (0700) at that step; an existing
//     one is verified and keeps its mode (plan.OpenSecureStore never chmods a
//     directory it did not create).
//   - The staging directory (in $TMPDIR, created only when a blob is written)
//     is removed on every return path and when a task body calls logger.Fatal.
//     A process killed by a signal it does not handle, or by SIGKILL, leaves
//     it behind.
//
// Nested Run calls while recording append into the same plan (used by Aggregate).
func RecordPlan(planID, planDir string, taskNames ...string) ([]plan.Op, error) {
	if planDir == "" {
		return RecordPlanTo(planID, nil, taskNames...)
	}
	return stageBlobs(planID, planDir, taskNames)
}

// RecordPlanTo is like RecordPlan but packages blobs straight into store
// (disk or memory), with no staging. Pass a nil store only when tasks need no
// blob packaging.
//
// Before returning, the finished plan passes validateRecordedPlan: dangling
// or forward cross-chunk dependencies and cross-chunk change watches fail the
// record, so no plan that ApplyChunks or remote.Delivery.ToHost would refuse
// is ever written (`gonf plan`), shipped (push, cluster, fleet) or applied (Run).
//
// RecordPlanTo does NOT undo blob writes: a record that fails (the
// pre-flight refusal, a task-body error, a packaging error) may already have
// written blobs into store. That is safe only for storage the caller discards
// on error — the per-call temp dir of Run, the staging directory of RecordPlan
// or a MemoryStore of push/cluster/fleet. Never pass a directory that must
// survive a refused record unchanged (such as `gonf plan -o dir`); use
// RecordPlan for that.
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
		recordSessionDraft(d, store)
	})
	resource.SetPlanDraftAmender(func(d resource.PlanDraft) error {
		return amendRecordedDraft(d, store)
	})
	defer func() {
		resource.SetPlanDraftRecorder(nil)
		resource.SetPlanDraftAmender(nil)
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
	if err := validateRecordedPlan(ops); err != nil {
		return nil, err
	}
	return ops, nil
}

// recordSessionDraft is the session's draft recorder: it notes d.ID as
// recorded for this task body, lowers d through packageDraft and appends the
// op. The first packaging failure is stashed in recordingPackErr, and the
// session records nothing more after it.
func recordSessionDraft(d resource.PlanDraft, store plan.BlobStore) {
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
}

// amendRecordedDraft is the session's amend sink (resource.AmendRegistered):
// it lowers d through packageDraft — the same draftToOp path, known-kind
// check and privilege derivation as a freshly recorded draft — and replaces
// the op recorded earlier under d.ID via plan.AmendRecorded, which refuses
// across a when-block or privilege boundary and when the privilege it
// derives here differs from the recorded op's. After a packaging failure the
// session records nothing more, so there is nothing to amend either.
func amendRecordedDraft(d resource.PlanDraft, store plan.BlobStore) error {
	if recSession.recordingPackErr != nil {
		return nil
	}
	op, err := packageDraft(d, store)
	if err != nil {
		return err
	}
	return plan.AmendRecorded(d.ID, func(plan.Op) (plan.Op, error) { return op, nil })
}

// validateRecordedPlan runs the whole-plan pre-flights (plan.ValidateChunks:
// dangling and forward cross-chunk dependencies, and change-gated ops watching
// resources recorded in a different privilege chunk — change reports are
// chunk-local: each privilege chunk applies as its own plan.Apply invocation,
// and an elevated chunk is a separate sudo/doas process with a report of its
// own, so such a watch could never fire — and schema-20 requirement blocks
// nested under, or using, a condition other than a host fact, whose outcome
// could change during an apply) over a freshly recorded plan.
//
// It lives here, in the single place every recorded plan passes through, for
// two reasons. (1) It is the earliest point all ops and their elevate flags
// are known, so Run, `gonf plan`, push, cluster and fleet all fail before the
// plan is written, shipped, or applied. (2) It is the only point that can
// protect `gonf plan` -> `gonf apply plan.jsonl`: the apply side cannot
// distinguish a whole plan from a single privilege chunk (the elevated
// re-exec child and remote pushes use the same `gonf apply <file|->` entry),
// so it cannot run the dangling-dependency check itself.
//
// The apply, push and preview side deliberately re-run the same check on the
// split plan (ApplyChunksContext, remote.Delivery.ToHost): a public entry point
// cannot assume its ops came from a record in this process (they may be decoded
// from a file or recorded by an older gonf), and the check is a cheap linear
// pass. For Run the second pass is therefore redundant by construction, never
// conflicting — the very same plan.ValidateChunks accepts both times.
//
// The refusal reads "RecordPlan: <reason>" with one prefix, worded in terms of
// registered resources (see preflightChunks); callers add their own context
// ("plan: ", "push: ") in front.
func validateRecordedPlan(ops []plan.Op) error {
	return preflightChunks("RecordPlan", fixHintRecord, plan.SplitPrivilegeChunks(ops))
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
		if err := recordTaskName(name); err != nil {
			return err
		}
	}
	return nil
}

// recordTaskName records one public task name. An Alias is pushed on the
// recording stack under its own name and then records its target, so a cycle
// that passes through an alias (t -> a -> t) is detected and named with the
// alias in the chain, while the ops are exactly the target's.
func recordTaskName(name string) error {
	if err := checkRecordingCycle(name); err != nil {
		// Task bodies cannot return errors; stash the cycle so every
		// enclosing body fails its record too.
		recSession.recordingCycleErr = err
		return err
	}
	target, isAlias, err := resolveAlias(name)
	if err != nil {
		return err
	}
	// The pop is deferred so a panicking task body (recovered by a caller,
	// e.g. a test) cannot leave its name on the stack, where it would be
	// blamed by later errors such as draftError's task prefix.
	recSession.recordingStack = append(recSession.recordingStack, name)
	defer func() {
		recSession.recordingStack = recSession.recordingStack[:len(recSession.recordingStack)-1]
	}()
	if isAlias {
		return recordTaskName(target)
	}
	return recordSingleTaskBody(name)
}

// recordNestedRun is Run's nested-session path: a task body running other
// tasks while a plan is being recorded. A failure is returned to the body
// and also stashed (propagateNestedRunError), so the enclosing record fails
// even when the body ignores the returned error — the silent `_ = Run(...)`
// pattern would otherwise drop the child's ops from the plan.
func recordNestedRun(names []string) error {
	err := recordTaskBodies(names)
	if err != nil {
		propagateNestedRunError(err)
	}
	return err
}

// propagateNestedRunError stashes a nested Run failure as the session's body
// error, naming the task body that ran it. Errors that already travel through
// a session stash (a cycle, a packaging failure, an earlier body error) are
// left alone: every enclosing body fails with them anyway, and re-wrapping
// would repeat the same context. The first failure wins, like stashBodyError.
func propagateNestedRunError(err error) {
	for _, stashed := range []error{recSession.recordingCycleErr, recSession.recordingPackErr, recSession.recordingBodyErr} {
		if stashed != nil && errors.Is(err, stashed) {
			return
		}
	}
	if recSession.recordingBodyErr != nil {
		return
	}
	recSession.recordingBodyErr = fmt.Errorf("task %s: %w", currentRecordingName(), err)
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
	defer func() { recSession.recordingElevate = prevElevate }()
	recSession.recordingElevate = c.privileged
	if len(wrapWhen) > 0 {
		plan.Record(plan.Op{
			Op:      plan.KindWhenBegin,
			ID:      "when." + name,
			All:     wrapWhen,
			Elevate: recSession.recordingElevate,
		})
	}

	if err := runTaskBody(c); err != nil {
		return err
	}
	if len(wrapWhen) > 0 {
		plan.Record(plan.Op{Op: plan.KindWhenEnd, Elevate: recSession.recordingElevate})
	}
	return nil
}

// runTaskBody runs c's body against a fresh resource repository and draft
// set, inside the aggregate dedupe scope that fits it (enterAggregateScope),
// and returns the session's stashed failure, if any, or the check for
// registered resources that produced no draft. Stashes stay set so every
// enclosing body fails with the same error.
func runTaskBody(c taskCandidate) error {
	resource.ResetRepository()
	resetRecordedDrafts()
	func() {
		defer enterAggregateScope(c.aggregate)()
		c.fn()
	}()
	for _, stashed := range []error{recSession.recordingCycleErr, recSession.recordingBodyErr, recSession.recordingPackErr} {
		if stashed != nil {
			return stashed
		}
	}
	return checkUnrecordedDrafts(c.name)
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
// session. Task bodies cannot return errors, so bodies that fail record it
// here (secret lookups, ForHosts; a failed nested Run is stashed by
// propagateNestedRunError instead); every enclosing body fails its
// record after its fn returns. The first error wins: a later one in the same
// session is a consequence or a second, independent failure, and the
// operator fixes the first one first. Aggregates use stashAggregateError,
// which adds their name to the chain instead.
func stashBodyError(err error) {
	if recSession.recordingBodyErr == nil {
		recSession.recordingBodyErr = err
	}
}

// stashAggregateError records an aggregate's failure as
// "aggregate <name>: <cause>". When cause is (or wraps) the error already
// stashed — a child body's failure propagating up — the stash is re-wrapped
// with the aggregate's name, so nested aggregates build the whole include
// chain (aggregate outer: aggregate inner: …). An unrelated later failure
// does not replace the first one.
func stashAggregateError(name string, cause error) {
	stashed := recSession.recordingBodyErr
	switch {
	case stashed == nil:
		recSession.recordingBodyErr = fmt.Errorf("aggregate %s: %w", name, cause)
	case errors.Is(cause, stashed):
		recSession.recordingBodyErr = fmt.Errorf("aggregate %s: %w", name, stashed)
	}
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
//
// ApplyPlan runs NO dangling-dependency pre-flight (plan.ValidateChunks),
// on purpose: it executes single privilege chunks too — the elevated re-exec
// child, one chunk of a remote push, `gonf apply <plan.jsonl|->` — and from
// one chunk it cannot tell a dep applied by an earlier chunk from a typo'd
// one, so such a dep is treated as satisfied. The same holds for the other
// chunk-level entry points, PushPayload and PushPayloadContext, which stream
// one already-encoded payload with an elevate flag. The guarantee lives where
// the whole plan is in hand: RecordPlanTo (record time: Run, `gonf plan`,
// push, cluster, fleet), ApplyChunks, remote.Delivery.ToHost and Apply. A
// caller feeding ApplyPlan (or PushPayload) a whole plan from elsewhere (a
// hand-written or older plan file) must run plan.ValidateChunks over
// plan.SplitPrivilegeChunks itself, or use ApplyChunks (PushTo), which does.
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

// draftError points a handler's record-time rejection at the recipe: the
// task being recorded (when a recording session is active; a local
// api.Apply has none) and the draft's resource ID, as
// "RecordPlan: [task %q: ]draft %q: <handler error>". The "RecordPlan:
// draft %q:" part matches draftToOp's own errors, which name no task; the
// task part mirrors checkUnrecordedDrafts. The handler's error is wrapped, so
// errors.Is/As still see it, and handlers must not add their own ID prefix.
func draftError(d resource.PlanDraft, err error) error {
	if len(recSession.recordingStack) == 0 {
		return fmt.Errorf("RecordPlan: draft %q: %w", d.ID, err)
	}
	return fmt.Errorf("RecordPlan: task %q: draft %q: %w", currentRecordingName(), d.ID, err)
}

// draftToOp lowers a resource draft to a plan op line by delegating to the
// draft kind's registered plan.Handler (see plan/handler.go): the resource
// package owns its own wire form and this function only folds in the
// recording session's Elevate flag. Every resource kind registers a Handler
// (see docs/plan.md, "Adding a resource kind"), so an unmapped kind is
// always a programming error (typo, or a new resource kind that forgot to
// register) and fails the record loudly here instead of silently forwarding
// an unknown op to the wire, where it would only blow up at remote apply
// time.
func draftToOp(d resource.PlanDraft) (plan.Op, error) {
	h, ok := plan.HandlerFor(plan.Kind(d.Kind))
	if !ok {
		return plan.Op{}, fmt.Errorf("RecordPlan: draft %q: unknown draft kind %q (no registered plan.Handler; see docs/plan.md kind checklist)",
			d.ID, d.Kind)
	}
	op, err := h.ToOp(d)
	if err != nil {
		return plan.Op{}, draftError(d, err)
	}
	op.Elevate = d.Elevate || recSession.recordingElevate
	if !plan.IsKnownKind(op.Op) {
		return op, fmt.Errorf("RecordPlan: draft %q: kind %q lowers to undeclared plan kind %q (missing from plan.AllKinds)",
			d.ID, d.Kind, op.Op)
	}
	return op, nil
}
