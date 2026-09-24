package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/snonux/gonf/internal/declerr"
	"github.com/snonux/gonf/internal/validator"
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
	// any task body (propagateNestedRunError), or a declaration error
	// reported while the body ran (internal/declerr, routed here by
	// RecordPlanTo's Capture): DSL misuse such as an unsupported option, a
	// secret lookup failure (MustSecret), or a ForHosts misuse or
	// missing/mistyped host value. Like the cycle stash, every enclosing body
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

	// needs is the Needs dedupe scope of the Run(...) list currently being
	// recorded (see needsScope in api/task_needs.go): which tasks that list
	// already recorded, so a needed task records once, before its first
	// dependent. recordTaskBodies installs a fresh scope per list, and a
	// non-aggregate task body starts with none (enterAggregateScope), for
	// the same envelope reason as aggregateSeen. It stays empty for plans
	// without Needs, whose recording it never changes.
	needs *needsScope
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
	s.needs = nil
}

// packager returns the draftPackager for a draft recorded right now in this
// session: it packages into store, claims blob refs in the session-wide
// recordedBlobRefs (so a ref collision is caught across every task body of
// the session), and carries the session's current elevate flag and innermost
// task name. It is built per draft, because both of those change as task
// bodies are entered and left.
func (s *recordingSession) packager(store plan.BlobStore) draftPackager {
	if s.recordedBlobRefs == nil {
		s.recordedBlobRefs = map[string]string{}
	}
	p := draftPackager{store: store, blobRefs: s.recordedBlobRefs, elevate: s.recordingElevate}
	if len(s.recordingStack) > 0 {
		p.task = s.recordingStack[len(s.recordingStack)-1]
	}
	return p
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
//     is removed on every return path: DSL misuse in a task body is a
//     returned record error (internal/declerr), never a process exit. A
//     process killed by a signal it does not handle, or by SIGKILL, leaves it
//     behind.
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
// Declaration errors (internal/declerr) fail the record too. One reported
// before the record — top-level registration misuse such as an empty Task
// name or a duplicate Host — refuses it before any task body runs. One
// reported while a task body runs — an option on a resource that does not
// support it, a failed MustSecret, a ForHosts misuse — is captured into this
// session (stashBodyError): the body keeps running, later declarations are
// still checked, and the record returns the first such error.
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
	if err := declerr.First(); err != nil {
		return nil, err
	}

	restore := resource.SnapshotRepository()
	defer func() {
		// A task body panic (a genuine programmer-bug invariant, not a
		// recipe error — see enterRecordMode's own comment anticipating
		// "a panic recovered outside RecordPlanTo") skips the normal
		// return entirely, so the restore below would never run without
		// this. Recovering, restoring, flagging, and re-panicking keeps
		// the documented panic-on-programmer-bug behavior (it still
		// crashes an uninstrumented process) while leaving the repository
		// AND lastRecordFailure consistent for any caller that does
		// recover it — go test's per-test recovery among them (tasks
		// cd2, jd2): whatever the panicked body itself registered does
		// not outlive the panic, and a caller that recovers and then
		// calls Apply() without an intervening clean record still gets
		// refused, the same guarantee the normal error return gets below.
		//
		// cd2 originally left lastRecordFailure unset here, reasoning a
		// caller able to recover a panic had already taken responsibility
		// for it — jd2 found that gap: with runTaskBody already having
		// wiped whatever was registered before this call, an unset flag
		// let a caller recover the panic, register something unrelated,
		// and reach a silently successful Apply() that had nothing to do
		// with what the recipe actually declared (the exact
		// partial-convergence shape ad2 closed on the error-return path).
		// TestPanickingTaskDoesNotLeakIntoLaterDraftErrors, which this was
		// once thought to conflict with, only pins that no STALE TASK
		// NAME leaks into a later, unrelated draft error — not that Apply
		// must succeed after a recovered panic; it now clears
		// lastRecordFailure itself, like a caller who deliberately moves
		// on after recovering, to reach that assertion.
		if r := recover(); r != nil {
			restore()
			lastRecordFailure = fmt.Errorf("RecordPlan %q: task body panicked: %v", planID, r)
			panic(r)
		}
	}()
	ops, err := recordPlanBody(planID, store, taskNames)
	if err == nil {
		// A declaration error can land on the process-wide sticky slot even
		// though recordPlanBody itself returned no error: something running
		// inside a task body during this very call (e.g. a defensive
		// resource.ResetForTest or resource.ResetDeclarationError call —
		// both are documented as meant for BETWEEN records, never during
		// one, but nothing before this check enforced that) can tear down
		// the capture sink enterRecordMode installed above
		// (declerr.Capture(stashBodyError)), so a LATER declaration error in
		// the same body — a failed MustSecret chief among them — misses
		// stashBodyError entirely and lands on declerr.First() instead. That
		// used to go unnoticed: recordPlanBody's own return was already nil
		// by then, and nothing afterward re-checked declerr.First(), so a
		// resource built from the failed call's inert zero-value return
		// (e.g. a File whose content embeds an empty MustSecret result) was
		// recorded and later applied as if nothing had gone wrong — see
		// TakeFirst's doc comment (internal/declerr) for the exact,
		// reproduced shape of this bug (task hg2), building on tf2's
		// narrower first-only clear, which closed one specific route
		// (ResetDeclarationError) into it. Re-checking here instead closes
		// the whole class regardless of how the sink was lost — a
		// structural fix rather than chasing each future route one at a
		// time. ops is discarded along with recordPlanBody's own nil
		// return: whatever it packaged cannot be trusted once a declaration
		// error surfaced anywhere during this call.
		if derr := declerr.First(); derr != nil {
			ops, err = nil, derr
		}
	}
	// Either outcome restores the repository to exactly what was
	// registered before this call started (tasks ad2/bd2, id2): this
	// record attempt's own registrations are done being useful to the
	// live repository the moment RecordPlanTo returns, since what they
	// became is already captured in ops (success) or lost with err
	// (failure) — nothing about them needs to additionally sit in the
	// repository afterward, on either path.
	//
	// On failure this replaces a blanket resource.ResetRepository(): a
	// task body that failed this record — declared misuse, a task
	// recursion cycle, a packaging error, a plan-level pre-flight refusal
	// — may already have registered resources before failing, and leaving
	// them registered would let a later api.Apply in this process
	// silently apply that half-declared set (task fc2). Wiping the WHOLE
	// repository was too broad, though: it also dropped whatever a
	// recipe had registered before this call ever started, and a later,
	// unrelated direct registration could then mask that loss by making
	// the repository look non-empty again (task ad2).
	//
	// On success this is new (task bd2): without it, a successful record
	// left exactly the last recorded scope's registrations (with their
	// drafts) sitting in the repository — e.g. the members of a
	// WhenHostname-guarded fragment that this very record's ops correctly
	// gated with when_begin/when_end — for a later, unguarded api.Apply
	// in the same process to lower and apply DIRECTLY, bypassing the
	// guard the ops encode. Restoring here means nothing a record leaves
	// behind can be blindly applied at all: what to do with the ops is
	// entirely the caller's job (RecordPlanTo just returns them).
	//
	// resource.SnapshotRepository (not an earlier, ID-based RollbackTo)
	// is what makes "restores to exactly what was registered before"
	// literally true: pruning down to a set of ID names could keep a
	// same-ID registration the record attempt itself made — with ITS new
	// value, not the original's — instead of restoring what was actually
	// there before, reopening bd2's bug one ID collision away (task id2).
	restore()
	if err != nil {
		// lastRecordFailure is the other half of the failure guard, for a
		// caller that skips straight to Apply without going through this
		// RecordPlanTo call at all; it covers every failure reason this
		// function can return, not only ones that reached declerr (task
		// tc2 — declerr only ever hears about declared misuse, never a
		// cycle or a packaging error, so gating solely on declerr left
		// those two reasons without the loud-refusal half of this guard,
		// even though the restore above already covered them). Storing
		// err itself, not just that some record failed, lets Apply's
		// refusal name the actual cause instead of an opaque sentence
		// (task uc2).
		lastRecordFailure = err
	} else {
		// A clean, complete record is itself evidence nothing is left
		// over from an earlier failure: an embedding program that fixed a
		// broken recipe must not stay refused by api.Apply forever over a
		// mistake it already recovered from (see lastRecordFailure).
		lastRecordFailure = nil
	}
	return ops, err
}

// lastRecordFailure holds the error of the last RecordPlanTo call that
// failed, for any of the reasons it can fail, and is cleared whenever one
// succeeds — never sticky for the life of the process the way declerr.First
// is, since a later clean record is itself proof nothing is left over from
// an earlier failure. api.Apply checks it UNCONDITIONALLY (task ad2
// reverted an earlier, narrower empty-repository-only scoping from task
// tc2, once it found that scoping let a later, unrelated registration mask
// an earlier one's silent loss — a resource registered before a failed
// Run/RecordPlanTo call used to be gone by the time that call returned,
// wiped by runTaskBody's per-task-body fresh repository with no restore).
// resource.SnapshotRepository (task id2) closed that specific loss —
// RecordPlanTo now restores the exact pre-call repository on every
// outcome, so a resource registered before the call genuinely survives a
// later, unrelated failure now — but the unconditional check stays rather
// than narrowing back: it costs an explicit recovery step (see below) for
// a real gain in robustness against whatever failure shape id2's fix does
// not happen to cover, and the alternative (checking only when the
// repository is empty) is the exact shape that already needed reverting
// once. The ONLY way to clear this is a later record that actually
// succeeds, never merely registering or applying something new directly.
// A test or caller that intentionally fails a record and continues in the
// same process must therefore call api.ResetForTest (or record cleanly
// again) rather than relying on a fresh registration alone — see
// api/reset.go and AGENTS.md's Test seams section for the tests this can
// affect under -shuffle=on.
//
// A bare package var, not mutex-guarded (task ed2): safe today because
// recording is single-goroutine (fleet records centrally before fan-out,
// the same invariant recordingSession's own doc states), so a future
// concurrent record must not read or write this without adding one.
var lastRecordFailure error

// recordPlanBody is RecordPlanTo's actual recording, split out so
// RecordPlanTo has one place to react to its error return (resetting the
// resource repository — see there) instead of a reset at every return
// statement here.
func recordPlanBody(planID string, store plan.BlobStore, taskNames []string) ([]plan.Op, error) {
	defer enterRecordMode(store)()
	if err := recordTaskBodies(taskNames); err != nil {
		return nil, err
	}
	if recSession.recordingPackErr != nil {
		return nil, recSession.recordingPackErr
	}

	ops := plan.FinishRecord(planID)
	plan.ResetRecord()
	// Resource ops were scanned as their drafts were packaged; control ops
	// (when blocks, requirements) are recorded directly and scanned here.
	if err := markRecordedControlOps(ops); err != nil {
		return nil, err
	}
	if err := validateRecordedPlan(ops); err != nil {
		return nil, err
	}
	return ops, nil
}

// enterRecordMode starts a fresh recording session packaging into store: it
// switches on plan recording, resets the session, installs the session's
// draft recorder and amend sink, and captures declaration errors
// (internal/declerr) into the session (stashBodyError), so DSL misuse inside
// a task body fails this record. The returned function leaves record mode
// again; RecordPlanTo defers it, so it runs even when a task body panics.
func enterRecordMode(store plan.BlobStore) (exit func()) {
	plan.ResetRecord()
	plan.SetRecording(true)
	// Defensive: recSession.reset() is a general safeguard against state an
	// earlier session left behind if it never reached its normal cleanup
	// (e.g. a panic recovered outside RecordPlanTo, such as by go test's
	// per-test recovery, which keeps running afterward). The stack pop
	// itself is not at risk here: recordTaskName pops its own entry in a
	// defer, so a panicking task body cannot leak a stale stack entry.
	recSession.reset()
	resource.SetPlanDraftRecorder(func(d resource.PlanDraft) {
		recordSessionDraft(d, store)
	})
	resource.SetPlanDraftAmender(func(d resource.PlanDraft) error {
		return amendRecordedDraft(d, store)
	})
	restoreDeclErr := declerr.Capture(stashBodyError)
	return func() {
		restoreDeclErr()
		resource.SetPlanDraftRecorder(nil)
		resource.SetPlanDraftAmender(nil)
		plan.SetRecording(false)
	}
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
	op, err := recSession.packager(store).packageDraft(d)
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
	op, err := recSession.packager(store).packageDraft(d)
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
// error contract (docs/design/plan.md "Error handling contract"). action names the
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
// recordingPackErr, so nested Run bodies see the real error too. The list
// is its own Needs scope: a name a Needs already recorded earlier in the
// list is skipped (skipNeeded), every other name records exactly as it did
// before Needs existed.
func recordTaskBodies(taskNames []string) error {
	defer enterNeedsScope()()
	for _, name := range taskNames {
		if skipNeeded(name) {
			continue
		}
		if err := recordTaskName(name); err != nil {
			return err
		}
		noteRecorded(name)
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
	// Needed tasks record first, outside this task's own when_begin and
	// privilege, while name is on the stack so a need that leads back to it
	// is a cycle (recordNeeds).
	if err := recordNeeds(name); err != nil {
		return err
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
	if c.hasOpaqueWhen() && len(wrapWhen) == 0 {
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
// session. Task bodies cannot return errors, so the declaration errors they
// report (internal/declerr: DSL misuse, secret lookups, ForHosts) arrive here
// through RecordPlanTo's Capture (a failed nested Run is stashed by
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
//
// ApplyPlan is not cancelable (ApplyPlanContext with context.Background());
// its signature is kept for API stability.
func ApplyPlan(ops []plan.Op, planDir string) error {
	return ApplyPlanContext(context.Background(), ops, planDir)
}

// ApplyPlanContext is ApplyPlan canceled by ctx (plan.ApplyWithContext):
// canceling ctx, e.g. the CLI's SIGINT/SIGTERM context, stops the backend
// command in flight (SIGTERM, SIGKILL after its grace) and starts no further
// op; the error wraps ctx.Err(). A validator running at the interrupt is not
// stopped (it stays bounded by the command timeout), but its verdict is
// discarded and the candidate not published (internal/validator runIn); a
// one-line notice on stderr says the apply waits for it
// (noteValidatorWait). It is what `gonf
// apply <plan.jsonl|->` (also the receiving end of a push) and the
// in-process chunks of ApplyChunksContext run.
func ApplyPlanContext(ctx context.Context, ops []plan.Op, planDir string) error {
	defer noteValidatorWait(ctx, os.Stderr, validator.Running)()
	return plan.ApplyWithContext(ctx, ops, toPlanFacts(DetectFacts()), planDir)
}

// planWhenForCandidate returns the serializable when predicates to record as
// a when_begin guard, or nil when the task has none.
//
// The opaque (non-serializable, e.g. a custom When(func)) predicates in
// c.opaque are evaluated here as a controller-side filter: they gate whether
// recording proceeds at all (an error when one fails), but they never take
// the place of the serializable guards — those are always emitted when
// present, even alongside an opaque predicate, so a task built with
// WhenLinux() + When(fn) still ships its goos guard. The serializable guards
// themselves are not evaluated on the controller (task 8h2): a mixed task
// records on a controller where only its opaque part holds, and the
// destination decides the rest.
func planWhenForCandidate(c taskCandidate) ([]plan.Predicate, error) {
	if c.hasOpaqueWhen() && !whenPasses(c.opaque, DetectFacts()) {
		return nil, fmt.Errorf("RecordPlan: task %q When predicates fail on controller and are not serializable", c.name)
	}
	if len(c.planWhen) == 0 {
		return nil, nil
	}
	out := make([]plan.Predicate, len(c.planWhen))
	copy(out, c.planWhen)
	return out, nil
}

// blobName returns the blob ref name for d: a human-readable basename (the
// destination's last path segment, when there is one) followed by a short
// hash of blobIdentityKey(d). The hash is what actually guarantees
// uniqueness — two drafts whose destinations merely share a basename (e.g.
// Dir(/x/conf.d, WithSource(a)) and Dir(/y/conf.d, WithSource(b)), or two
// >512KiB Files with equal basenames) previously both packaged to
// "blobs/conf.d", so the second store.Write* call silently overwrote the
// first one's content; draftPackager.guardBlobRef (api/packager.go) is the
// defense-in-depth backstop in case a future change reintroduces a real
// collision anyway.
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
	if path := sourceFilePath(d); path != "" {
		return filepath.Base(path)
	}
	sourceDir, sourceGlob := syncDirSource(d)
	if sourceDir != "" {
		return filepath.Base(sourceDir)
	}
	if sourceGlob != "" {
		dir := filepath.Dir(sourceGlob)
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
	sourceDir, sourceGlob := syncDirSource(d)
	return strings.Join([]string{d.Path, sourceFilePath(d), sourceDir, sourceGlob}, "\x00")
}

// shortHash returns a short, fixed-width hex fingerprint of key, used to
// disambiguate blob refs that would otherwise share a human-readable
// basename. It is not required to be stable across gonf versions or plan
// runs — docs/design/plan.md already treats blob paths as ephemeral, record-time
// artifacts, e.g. the source_dir field exists precisely so destination apply
// never depends on the blob path staying the same between runs.
func shortHash(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])[:8]
}
