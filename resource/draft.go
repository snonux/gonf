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
	// Path is the destination path for the file/dir/link/sync/ensure kinds.
	Path string

	// Symlink is the symlink target for the "link" kind.
	Symlink string
	// Hardlink is the hardlink target when set instead of Symlink.
	Hardlink string
	// Target is the existence-checked path for link_if_exists drafts.
	Target string

	// Mode is an octal permission string such as "0640" for Path.
	Mode string
	// FileMode is an octal permission string for files copied from
	// SourceDir or SourceGlob.
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
	// SourceDir is a controller-local directory to package as a blob tree.
	SourceDir string
	// SourceGlob is a controller-local glob to package as a flat blob dir.
	SourceGlob string
	// Prune removes destination entries not present in the source.
	Prune bool
	// Absent marks NoFile/NoDir/NoLink/NoPackage style removal.
	Absent bool

	// AddLine appends a line to a file when missing (line-in-file).
	AddLine string
	// RemoveLine removes matching lines from a file.
	RemoveLine string

	// Name is a package name, command registry name, or similar label.
	Name string
	// Bin is the executable for the "command" kind.
	Bin string
	// Args is argv after Bin for the "command" kind.
	Args []string
	// Dir is the working directory for the "command" kind.
	Dir string
	// Env is extra environment for the "command" kind.
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
	// Schedule holds the five space-separated cron time fields
	// (minute hour monthday month weekday) for cron drafts.
	Schedule string
	// CronEnv lists KEY=VAL environment lines above the cron job.
	CronEnv []string
	// Restart restarts a service once when it is already running.
	Restart bool
	// Reload reloads a service once when it is already running
	// (no restart fallback).
	Reload bool
	// EnableOnly skips start/stop for timer present (enable/disable only).
	EnableOnly bool
	// IfChanged gates daemon_reload on watched dependency outcomes.
	IfChanged bool
	// Watch lists resource ids for IfChanged (usually DependsOn targets).
	Watch []string

	// Elevate forces elevate=true on this draft (e.g. options.WithElevate).
	Elevate bool
}

var (
	draftMu       sync.Mutex
	draftRecorder func(PlanDraft)
)

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
	draftMu.Lock()
	fn := draftRecorder
	draftMu.Unlock()
	if fn != nil {
		fn(draft)
	}
}
