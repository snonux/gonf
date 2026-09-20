package resource

import "sync"

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

// PlanDraft is a package-neutral snapshot of a registered resource for plan
// recording. The api package converts drafts to plan.Op so resource packages
// do not import plan (avoids cycles with plan apply helpers).
type PlanDraft struct {
	// Kind selects the op kind the recorder emits, e.g. "file" or "cron".
	Kind string

	// ID is the registered resource ID this draft came from.
	ID string
	// Path is the destination path for the file/dir/link/sync/ensure_dir/
	// ensure_file kinds.
	Path string

	// Symlink is the symlink target for the "link" kind.
	Symlink string
	// Hardlink is the hardlink target when set instead of Symlink.
	Hardlink string
	// Target is the existence-checked path for link_if_exists drafts.
	Target string

	// Mode is an octal permission string such as "0640" for Path; four digits
	// (e.g. "04755") when setuid/setgid/sticky are set.
	Mode string
	// FileMode is an octal permission string for files copied from
	// SourceDir or SourceGlob (same format as Mode).
	FileMode string
	// Owner is the explicitly configured owning user (WithOwner) for the
	// file/dir/sync_dir/ensure_dir kinds. Empty means not configured, so
	// destination apply leaves ownership as-is instead of chowning to the
	// build-time default user.
	Owner string
	// Group is the explicitly configured owning group (WithGroup) for the
	// file/dir/sync_dir/ensure_dir kinds (name or numeric id). Empty means
	// not configured.
	Group string
	// ContentB64 is base64 file content for the "file" kind.
	ContentB64 string
	// Blob is a sidecar blob reference for the sync_dir kind (or large
	// "file" content).
	Blob string
	// SourcePath is a controller-local file to package as content_b64 or a blob.
	SourcePath string
	// HasContent marks that the "file" kind's content was explicitly
	// configured (WithContent or WithSource), even when the resulting bytes
	// are empty. It lets apply tell a legitimately empty file apart from an
	// op that is missing content data outright (a record-time bug): only
	// the latter should fail loudly with "missing content_b64 and blob".
	HasContent bool
	// Template marks that the "file" kind's content must be rendered as a
	// text/template on the destination: the recipe's source or path ended
	// in ".tmpl". draftToOp copies it onto the file op's template field so
	// plan apply renders it, since by apply time the wire content is raw
	// template text with no ".tmpl"-suffixed path left to detect it from.
	Template bool
	// TemplateParam is the declared source path used as the template's
	// {{.Param}} default when Template is set — the same value a direct
	// (non-plan) File with the same ".tmpl" source would use, so plan apply
	// renders the identical {{.Param}} a local run would instead of exposing
	// a plan-apply implementation detail (there is no destination-side
	// source file to derive it from).
	TemplateParam string
	// TemplateData is the JSON-compatible data supplied by WithTemplateData.
	// The file handler encodes it so invalid values fail during RecordPlan.
	TemplateData    any
	TemplateDataSet bool
	// SourceDir is a controller-local directory to package as a blob tree;
	// for sync_dir drafts it also carries the recipe's DECLARED source
	// directory onto the op's source_dir field (the glob pattern's
	// directory for the glob flavor, where packaging stays driven by
	// SourceGlob): destination apply renders tree .tmpl files' {{.Param}}
	// from it instead of the ephemeral blob path.
	SourceDir string
	// SourceGlob is a controller-local glob to package as a flat blob dir.
	SourceGlob string
	// Prune removes destination entries not present in the source.
	Prune bool
	// Absent marks NoFile/NoDir/NoLink/NoPackage style removal.
	Absent bool
	// Latest marks a "package" draft configured with IsLatest: destination
	// apply must run the backend's upgrade-check path (dnf update / pkg
	// upgrade / pkg_add -u / pkgin install) instead of a plain install.
	Latest bool

	// PrimaryGroup and SupplementaryGroups describe a "user" draft. The
	// resource only adds missing supplementary memberships; it never changes
	// an existing account's primary group or removes memberships.
	PrimaryGroup        string
	SupplementaryGroups []string
	// Home, CreateHome, Shell, LoginClass, and System are creation-time user
	// attributes. They are retained on the wire so destination apply makes the
	// same decision for a missing account as a direct resource apply.
	Home       string
	CreateHome bool
	Shell      string
	LoginClass string
	System     bool

	// AddLines appends lines to a file when missing (line-in-file).
	AddLines []string
	// RemoveLines removes matching lines from a file.
	RemoveLines []string
	// AddLine and RemoveLine are retained for compatibility with older draft
	// producers. New file resources use AddLines and RemoveLines.
	AddLine    string
	RemoveLine string

	// Name is a package name, command registry name, file resource identity,
	// or similar label. A named file keeps its identity independently of Path.
	Name string
	// Bin is the executable for the "command" kind.
	Bin string
	// Args is argv after Bin for the "command" kind.
	Args []string
	// Dir is the working directory for the "command" kind.
	Dir string
	// Env is extra environment for the "command" and "package" kinds.
	Env map[string]string
	// Creates skips the command when this path already exists.
	Creates string
	// Unless skips the command when the guard probe succeeds.
	Unless *PlanGuardDraft
	// OnlyIf runs the command only when the guard probe succeeds.
	OnlyIf *PlanGuardDraft

	// User selects systemd --user for timer / daemon_reload / service drafts.
	User bool
	// CronUser is the crontab owner for cron drafts (default root).
	CronUser string
	// Command is the crontab command for cron drafts.
	Command string
	// LegacyCommand opts a cron draft into removing one exact unmanaged command.
	LegacyCommand string
	// Schedule holds the five space-separated cron time fields
	// (minute hour monthday month weekday) for cron drafts.
	Schedule string
	// CronEnv lists KEY=VAL environment lines above the cron job.
	CronEnv []string
	// OnCalendar is the systemd OnCalendar= expression for systemd_timer drafts.
	OnCalendar string
	// OnBootSec is the systemd OnBootSec= delay for systemd_timer drafts.
	OnBootSec string
	// Persistent sets Persistent=true on systemd_timer drafts.
	Persistent bool
	// Description is the [Unit] Description for systemd_timer drafts.
	Description string
	// ServiceDescription is the companion oneshot .service Description.
	ServiceDescription string
	// After lists After= dependencies on the companion oneshot .service.
	After []string
	// Wants lists Wants= dependencies on the companion oneshot .service.
	Wants []string
	// Restart restarts the unit once when it is already running
	// (service or timer drafts).
	Restart bool
	// Reload reloads a service once when it is already running
	// (no restart fallback).
	Reload bool
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
	// plan apply orders ops like the repository path does.
	Deps []string

	// Elevate forces elevate=true on this draft (e.g. options.WithElevate).
	Elevate bool
}

var (
	draftMu       sync.Mutex
	draftRecorder func(PlanDraft)
)

// The draft recorder, plan recording (plan.SetRecording), and the api
// recording session form the record-mode trio: RecordPlanTo (api/plan.go)
// always installs and uninstalls all three together for one recording
// session. It lives here because resource cannot import api or plan
// without a cycle; the trio relationship is documented here and in
// plan/record.go.

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

// RecordPlanDraft forwards draft to the installed recorder when present.
func RecordPlanDraft(draft PlanDraft) {
	getRepository().recordDraft(draft)

	draftMu.Lock()
	fn := draftRecorder
	draftMu.Unlock()
	if fn != nil {
		fn(draft)
	}
}

// RegisteredPlanDrafts returns the plan drafts emitted by the currently
// registered resources, sorted by resource ID. The api package uses this
// snapshot for its direct Apply compatibility path; plan recording sessions
// continue to receive drafts through SetPlanDraftRecorder as before.
func RegisteredPlanDrafts() []PlanDraft {
	return getRepository().draftsSnapshot()
}
