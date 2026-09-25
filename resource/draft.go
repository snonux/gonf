package resource

import (
	"slices"
	"sync"
)

// PlanGuardDraft is a package-neutral command probe for plan recording.
type PlanGuardDraft struct {
	// Bin is the probe executable.
	Bin string
	// Args is argv after Bin.
	Args []string
	// ExpectStdout, when non-empty, is the trimmed stdout the probe must print.
	ExpectStdout string
	// ExpectExit is the exit code that makes the probe succeed. Nil means 0.
	ExpectExit *int
}

// ClonePlanGuardDraft deep-copies a guard probe, including its Args and
// ExpectExit (nil stays nil). Exported (task 2e2) so every kind package
// that embeds a *PlanGuardDraft in its own DraftPayload (currently
// resource/cmd's Payload, for the Unless/OnlyIf guards) clones it through
// this one definition next to the type, instead of a private per-package
// copy: resource genuinely cannot reach INTO a kind package's payload type
// to clone it generically, but it can export a clone function for its own
// type here, which every consumer can call.
func ClonePlanGuardDraft(g *PlanGuardDraft) *PlanGuardDraft {
	if g == nil {
		return nil
	}
	c := *g
	c.Args = slices.Clone(g.Args)
	if g.ExpectExit != nil {
		exit := *g.ExpectExit
		c.ExpectExit = &exit
	}
	return &c
}

// KeyedLine is one WithKeyedLine edit in a plan draft: Line owns the file's
// one line starting with the literal prefix Key. plan.KeyedLine is its wire
// twin (plan imports this package, so the draft cannot use the wire type).
type KeyedLine struct {
	Key  string
	Line string
}

// Block is one WithBlock managed block in a plan draft: Lines own the file's
// lines between the "# BEGIN GONF <Name>" and "# END GONF <Name>" markers.
// plan.Block is its wire twin.
type Block struct {
	Name  string
	Lines []string
}

// DraftPayload is a per-kind extension to PlanDraft (task w62, "Layer 1" of
// splitting the audited PlanDraft/plan.Op god structs): a resource kind that
// has migrated off the flat kind-exclusive fields below sets Payload to its
// own concrete type (e.g. cron.Payload) instead of writing those fields
// directly, so its planDraft() builder and its Handler.ToOp gain compiler
// protection against touching another kind's field — the flat fields could
// not offer that, which was exactly the audited defect ("no kind's payload
// is type-checked against its own kind"). Payload is nil for a draft whose
// kind has not migrated yet; PlanDraft's remaining flat fields stay the
// source of truth for those kinds, and for the small set of fields multiple
// kinds genuinely share with the same meaning (Path, Mode, Owner, Group,
// Name, Absent, Deps, Sensitive, Elevate, ...), which stay flat by design
// rather than being duplicated per kind. This interface, and the concrete
// payload types that implement it, live in this package rather than
// resource/<kind> because PlanDraft (declared here) must reference the
// interface type; the concrete per-kind struct still lives in its owning
// resource/<kind> package (e.g. resource/cron.Payload) and only needs to
// implement this one small interface to slot into Payload — resource/<kind>
// already imports this package for PlanDraft itself, so this adds no new
// import edge.
type DraftPayload interface {
	// Clone returns a deep copy: every slice, map or pointer the payload
	// holds gets its own backing storage. PlanDraft.Clone calls this to
	// deep-copy Payload without knowing its concrete type, mirroring how
	// Clone below deep-copies PlanDraft's own slice/map/pointer fields.
	Clone() DraftPayload
}

// SourceDirPayload is implemented by a migrated kind's DraftPayload that
// carries a controller-local source directory/glob to package as a blob
// (currently only resource/dir's SyncPayload, for the "sync_dir" kind). It
// lives in this kind-neutral core package, not in resource/dir, so
// packaging code that must stay kind-neutral (internal/testapply, whose own
// package doc says it imports only plan and resource precisely so it never
// creates a cycle with a resource/<kind> package's own tests) can
// type-assert it without importing resource/dir back.
// A caller must check d.Kind == "sync_dir" before this assertion, not rely
// on the assertion alone (task 0e2): the assertion only tests d.Payload's
// concrete TYPE, so an unrelated kind whose payload happens to grow a
// like-named SourceDirGlob() method for its own purpose would otherwise be
// consulted too, silently packaging controller-local directory bytes into
// that kind's op. api/packager.go's syncDirSource and internal/testapply's
// packageSource are the only two consumers and both gate this way;
// api/plan_fitness_test.go's TestSourcePayloadFitness pins the exact set of
// payload types allowed to implement this interface at all, as defense in
// depth alongside the caller-side gate.
type SourceDirPayload interface {
	DraftPayload
	// SourceDirGlob returns the packageable source: SourceDir (a
	// directory), or SourceGlob (a glob pattern) when the draft was built
	// from WithSourceGlob. At most one is ever non-empty.
	SourceDirGlob() (sourceDir, sourceGlob string)
}

// SourceFilePayload is implemented by a migrated kind's DraftPayload that
// carries a controller-local source FILE to package as content_b64 or a
// blob (currently only resource/file's Payload, for the "file" kind).
// Mirrors SourceDirPayload's role for the "sync_dir" kind, and lives here
// for the same reason: internal/testapply must stay kind-neutral (see its
// own package doc) and cannot import resource/file back.
// A caller must check d.Kind == "file" || d.Kind == "ensure_file" before
// this assertion, not rely on the assertion alone (task 0e2), for the same
// reason SourceDirPayload's doc above gives: the assertion only tests
// d.Payload's concrete TYPE, not that the draft's kind ever meant to carry a
// file source. api/packager.go's sourceFilePath and internal/testapply's
// packageSource are the only two consumers and both gate this way;
// TestSourcePayloadFitness (api/plan_fitness_test.go) pins the exact set of
// payload types allowed to implement this interface at all.
type SourceFilePayload interface {
	DraftPayload
	// SourceFilePath returns the packageable source file path, or "" for a
	// draft with no file source configured.
	SourceFilePath() string
}

// PlanDraft is a package-neutral snapshot of a registered resource for plan
// recording. It lives in this core package, not in plan, because plan
// imports this package (plan.Handler.ToOp takes a PlanDraft), so this
// package can never import plan back. The resource/<kind> backends sit
// above both: each imports plan and lowers its own drafts to plan.Op in its
// registered plan.Handler (<kind>/planwire.go); the api package only
// dispatches a draft to that Handler (draftToOp).
type PlanDraft struct {
	// Kind selects the op kind the recorder emits, e.g. "file" or "cron".
	Kind string

	// Payload carries the kind-exclusive fields of a migrated kind (see
	// DraftPayload). Nil for a kind that still uses the flat fields below.
	Payload DraftPayload

	// ID is the registered resource ID this draft came from.
	ID string
	// Path is the destination path for the file/dir/link/sync/ensure_dir/
	// ensure_file kinds.
	Path string

	// Mode is an octal permission string such as "0640" for Path; four digits
	// (e.g. "04755") when setuid/setgid/sticky are set.
	Mode string
	// Owner is the explicitly configured owning user (WithOwner) for the
	// file/dir/sync_dir/ensure_dir kinds. Empty means not configured, so
	// destination apply leaves ownership as-is instead of chowning to the
	// build-time default user.
	Owner string
	// Group is the explicitly configured owning group (WithGroup) for the
	// file/dir/sync_dir/ensure_dir kinds (name or numeric id). Empty means
	// not configured.
	Group string
	// Blob is a sidecar blob reference for the sync_dir kind (or large
	// "file" content). Filled in later by api's packageDraft, after ToOp
	// returns, for both kinds; stays flat because both reuse it.
	Blob string
	// Prune removes destination entries not present in the source (dir and
	// sync_dir drafts, both handled by resource/dir).
	Prune bool
	// Absent marks NoFile/NoDir/NoLink/NoPackage style removal.
	Absent bool

	// Name is a package name, command registry name, file resource identity,
	// or similar label. A named file keeps its identity independently of Path.
	Name string
	// Env is extra environment for the "command" and "package" kinds.
	Env map[string]string

	// User selects systemd --user for timer / daemon_reload / service drafts.
	User bool
	// Command is the crontab command line for cron drafts, and the oneshot
	// .service ExecStart command for systemd_timer drafts (two kinds
	// reusing one field for the analogous "the command to run" meaning,
	// the same way Path/Mode/Owner/Group are reused across the filesystem
	// kinds). cron's own CronUser/LegacyCommand/Schedule/CronEnv fields
	// migrated into resource/cron.Payload (task w62 Layer 1); systemd_timer's
	// own exclusive fields (OnCalendar, OnBootSec, Persistent, Description,
	// ServiceDescription, After, Wants) migrated the same way into
	// resource/systemdtimer.Payload. Command itself stays flat by design,
	// not as an unfinished migration: both kinds genuinely share its "the
	// command to run" meaning, the same way Path/Mode/Owner/Group are
	// reused across the filesystem kinds.
	Command string
	// Restart restarts the unit once when it is already running
	// (service or timer drafts).
	Restart bool
	// EnableOnly skips start/stop for timer present (enable/disable only).
	EnableOnly bool
	// IfChanged arms the draft's change gate (the OnChange option): a gated
	// command is skipped entirely, a service/timer's restart/reload action
	// is held, and a daemon-reload is skipped, unless one of the Watch ids
	// reported a change during this apply.
	IfChanged bool
	// Watch lists resource ids for IfChanged (the OnChange targets; usually
	// also DependsOn targets for daemon_reload).
	Watch []string
	// Deps lists the sorted resource IDs this draft's resource depends on
	// (the DependsOn targets). draftToOp copies them into plan.Op.Deps so
	// plan apply orders ops by their dependencies.
	Deps []string

	// Elevate forces elevate=true on this draft (e.g. options.WithElevate).
	Elevate bool
	// Sensitive declares the draft's payload secret material
	// (options.WithSensitive, embed.Sensitivity). api's draft packager ORs
	// it into plan.Op.Sensitive next to what the secret scan detects, so it
	// can only add sensitivity; false leaves the op exactly as the scan
	// decides.
	Sensitive bool
}

var (
	draftMu       sync.Mutex
	draftRecorder func(PlanDraft)
)

// The draft recorder, the draft amender (SetPlanDraftAmender, resource/
// amend.go), plan recording (plan.SetRecording) and the api recording
// session form the record-mode set: RecordPlanTo (api/plan.go) always
// installs and uninstalls all four together for one recording session. The
// recorder and amender live here because resource cannot import api or plan
// without a cycle; the relationship is documented here, in plan/record.go
// and in api/plan.go.

// SetPlanDraftRecorder installs the sink used by RecordPlanDraft.
// Pass nil to disable recording.
func SetPlanDraftRecorder(fn func(PlanDraft)) {
	draftMu.Lock()
	defer draftMu.Unlock()
	draftRecorder = fn
}

// PlanDraftRecording reports whether a draft recorder is installed.
func PlanDraftRecording() bool {
	draftMu.Lock()
	defer draftMu.Unlock()
	return draftRecorder != nil
}

// RecordPlanDraft stores draft for its registered resource (the snapshot
// RegisteredPlanDrafts returns) and forwards it to the installed recorder
// when present. The store and the recorder each receive their own deep copy
// (PlanDraft.Clone), so neither shares a slice, map or guard with the
// caller's draft or with each other: a later mutation on any side cannot
// change what the others lower to a plan op.
func RecordPlanDraft(draft PlanDraft) {
	getRepository().recordDraft(draft.Clone())

	draftMu.Lock()
	fn := draftRecorder
	draftMu.Unlock()
	if fn != nil {
		fn(draft.Clone())
	}
}

// RegisteredPlanDrafts returns the plan drafts emitted by the currently
// registered resources, sorted by resource ID. Each is a deep copy
// (PlanDraft.Clone) the caller owns: mutating it changes neither the store
// nor a later snapshot. The api package uses this snapshot for its direct
// Apply compatibility path; plan recording sessions continue to receive
// drafts through SetPlanDraftRecorder as before.
func RegisteredPlanDrafts() []PlanDraft {
	return getRepository().draftsSnapshot()
}
