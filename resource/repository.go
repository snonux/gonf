package resource

import (
	"fmt"
	"sort"
	"sync"

	"github.com/snonux/gonf/internal/logger"
)

// The DSL registries are deliberately single-goroutine: recipe construction
// happens before fleet fan-out (PushCluster records centrally, then streams
// the same bytes over SSH), so nothing in this file is safe for concurrent
// registration or reset. repoMu guards only the repo pointer itself
// (getRepository reads, ResetRepository swaps); repository.mu guards the
// contents of one repository instance.
var (
	repo   *repository
	repoMu sync.Mutex
)

func getRepository() *repository {
	repoMu.Lock()
	defer repoMu.Unlock()
	if repo == nil {
		repo = newRepository()
	}
	return repo
}

// ResetRepository swaps in a fresh empty repository. Tests use it to
// isolate registrations between runs; resource.ResetForTest is the
// canonical single entry point for resetting all resource state.
//
// The swap takes repoMu so it cannot race a concurrent getRepository read
// handing out the previous pointer. Callers keep their snapshot (the old
// repository stays valid) until they re-read getRepository; registration
// and apply remain single-goroutine by DSL invariant.
func ResetRepository() {
	repoMu.Lock()
	defer repoMu.Unlock()
	repo = newRepository()
}

// SnapshotRepository swaps in a fresh empty repository and returns a
// restore func that swaps the ORIGINAL one back, discarding whatever the
// fresh one accumulated in between. api.RecordPlanTo uses it (tasks
// ad2/bd2, id2) to undo exactly what one record attempt itself registered
// — on every outcome (success, failure, or a recovered panic), since the
// record's own findings are already captured in its returned ops (or lost
// with its error) by the time it returns, nothing about its own
// registrations needs to survive in the live repository — while leaving
// whatever was registered BEFORE that attempt started untouched, verbatim.
//
// This replaced an earlier, ID-based RollbackTo(kept []string) (tasks
// ad2/bd2): pruning down to a set of ID names, rather than restoring the
// actual pre-snapshot values, meant a resource the record attempt
// registered under an ID that collided with one from BEFORE the snapshot
// was silently KEPT (with the record's own, new value) instead of the
// original being restored — reopening the exact when-guard-bypass bd2
// fixed, just one ID collision away (task id2). Swapping the whole
// repository pointer back cannot have that failure mode: there is no
// per-ID merge decision to get wrong.
//
// Single-goroutine by the same DSL invariant every other repository
// primitive relies on: nothing may register concurrently with a snapshot
// still outstanding, and only one snapshot may be outstanding at a time
// (restoring an outer one after an inner one already restored would lose
// the inner scope's own restore).
func SnapshotRepository() (restore func()) {
	repoMu.Lock()
	saved := repo
	if saved == nil {
		saved = newRepository()
	}
	repo = newRepository()
	repoMu.Unlock()
	return func() {
		repoMu.Lock()
		repo = saved
		repoMu.Unlock()
	}
}

// repository is one recipe scope's registry: the registered resources (by
// ID, with their dependency edges and registered value), the plan draft each
// one recorded, and (direct-apply path only) the When*/WhenPathExists
// condition active when each ID was first registered (declaredUnder), so a
// same-ID collision can name it. whenStack is the LIFO of currently-active
// When*/WhenPathExists conditions on that same direct-apply path (see
// PushWhenContext below): it is per-recipe-scope state exactly like
// declaredUnder, so it lives here rather than as a separate package-level
// global — a repository swap (ResetRepository, SnapshotRepository, and so
// ResetForTest) now discards it along with everything else this scope
// tracked, instead of leaking a stale condition into a later scope. It
// applies nothing itself; api.Apply and api.Run lower the drafts to plan ops
// and apply them through the plan engine (the direct repository apply path
// was retired in task e72).
type repository struct {
	registered    map[string]Resource
	drafts        map[string]PlanDraft
	declaredUnder map[string]string
	whenStack     []string
	mu            sync.Mutex
}

func newRepository() *repository {
	return &repository{
		registered:    make(map[string]Resource),
		drafts:        make(map[string]PlanDraft),
		declaredUnder: make(map[string]string),
	}
}

func (r *repository) register(res Resource) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.registered[res.ID()]; exists {
		return collisionError(res, r.declaredUnder[res.ID()], r.currentWhenContext())
	}

	r.registered[res.ID()] = res
	r.declaredUnder[res.ID()] = r.currentWhenContext()
	logger.Debug("Registered resource %v", res)

	return nil
}

// RegisteredIDs returns the sorted IDs of all currently registered resources.
// The plan recorder uses it to fail loudly when a registered resource kind
// produced no plan draft (it would otherwise be silently skipped by apply).
func RegisteredIDs() []string {
	return getRepository().registeredIDs()
}

func (r *repository) registeredIDs() []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	ids := make([]string, 0, len(r.registered))
	for id := range r.registered {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// recordDraft stores draft for its registered resource. The caller hands
// over ownership: RecordPlanDraft passes a fresh PlanDraft.Clone.
func (r *repository) recordDraft(draft PlanDraft) {
	if draft.ID == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, registered := r.registered[draft.ID]; !registered {
		return
	}
	r.drafts[draft.ID] = draft
}

// draftsSnapshot returns deep copies (PlanDraft.Clone) of the stored drafts,
// sorted by resource ID, so a caller mutating the snapshot cannot reach into
// the store.
func (r *repository) draftsSnapshot() []PlanDraft {
	r.mu.Lock()
	defer r.mu.Unlock()

	ids := make([]string, 0, len(r.drafts))
	for id := range r.drafts {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	drafts := make([]PlanDraft, 0, len(ids))
	for _, id := range ids {
		drafts = append(drafts, r.drafts[id].Clone())
	}
	return drafts
}

// collisionError composes the "already registered" error for a duplicate
// ID. On the direct (non-recording) apply path several
// WhenHostname/WhenPathExists fragments can legitimately match the SAME
// host and so run their fn() bodies, one after another, into this one
// repository (task kd2 removed the per-fragment reset that used to paper
// over that by silently discarding whatever an earlier fragment had
// registered; see api/when_hostname.go and api/when_path.go). When that
// produces the same resource ID twice, first and second name the
// When*/WhenPathExists condition active for the original and for the
// colliding declaration ("" when a declaration happened outside any
// When*/WhenPathExists — a plain top-level duplicate, unrelated to
// overlapping fragments). Naming both means the operator sees which two
// conditions collided instead of a bare "already registered" — still true
// even when this exact error later resurfaces on a completely unrelated
// registration, because declerr's first report is sticky for the process
// (see AGENTS.md's "Registration-time contract"): the message names its
// own real cause, so it stays legible instead of misleadingly pointing at
// whatever registers next.
func collisionError(res Resource, first, second string) error {
	switch {
	case first != "" && second != "":
		return fmt.Errorf(
			"resource %v already registered (declared under %s; colliding declaration under %s — direct apply requires resource IDs to stay unique across every When*/WhenPathExists fragment that matches this host, unlike a recorded plan where each fragment keeps its own scope)",
			res, first, second)
	case second != "":
		return fmt.Errorf("resource %v already registered (colliding declaration under %s)", res, second)
	case first != "":
		return fmt.Errorf("resource %v already registered (first declared under %s)", res, first)
	default:
		return fmt.Errorf("resource %v already registered", res)
	}
}

// PushWhenContext records desc (e.g. `WhenHostname("web")`,
// `WhenPathExists("/etc")`) as the active When*/WhenPathExists condition on
// the CURRENT repository scope while its fn() runs on the direct
// (non-recording) apply path. If fn() registers a resource ID that another
// matching fragment already registered, collisionError names both
// conditions instead of a bare "already registered". The recording path
// never calls this: its when_begin/when_end ops already carry the condition
// in the recorded plan, and RecordPlan gives each fragment its own
// repository scope (ResetRepository per fragment), so a same-ID collision
// cannot happen there in the first place. The caller must defer the
// returned pop so nested/sibling fragments see a correctly balanced stack;
// even a dropped pop cannot outlive a repository swap, though (see
// repository.whenStack), so at worst it mislabels a collision within the
// SAME still-live scope, never across a ResetForTest or a later recipe run.
func PushWhenContext(desc string) (pop func()) {
	return getRepository().pushWhenContext(desc)
}

// pushWhenContext is PushWhenContext's implementation, scoped to this one
// repository instance. Registration is single-goroutine by the same DSL
// invariant the rest of this file relies on (see the package doc comment
// above), so no lock guards whenStack: the returned pop closes over r and
// the pushed index directly, so it stays correct even if getRepository()
// later hands out a different instance.
func (r *repository) pushWhenContext(desc string) (pop func()) {
	r.whenStack = append(r.whenStack, desc)
	i := len(r.whenStack) - 1
	return func() { r.whenStack = r.whenStack[:i] }
}

// currentWhenContext returns the innermost active When*/WhenPathExists
// condition on this repository, or "" when nothing is running inside one (a
// plain top-level registration, or the recording path, which never
// pushes).
func (r *repository) currentWhenContext() string {
	if len(r.whenStack) == 0 {
		return ""
	}
	return r.whenStack[len(r.whenStack)-1]
}
