// Package plan defines the versioned JSONL wire types and codec for remote
// gonf plan/apply (encode/decode, version gate).
//
// See the overall design plan (Remote gonf — plan/apply with serialized JSONL).
package plan

// CurrentVersion is the plan wire schema version emitted by gonf plan.
// Version 2 added timer and daemon_reload ops; version 3 added cron and
// service ops; version 4 added owner/group fields to the filesystem ops
// (file, dir, sync_dir, ensure_dir); version 5 added the deps field
// (recorded DependsOn ordering) to resource ops; version 6 added the
// source_dir field to sync_dir ops (the recipe's declared source directory,
// the stable {{.Param}} base for .tmpl files inside the synced tree);
// version 7 added the systemd_timer op (declarative timer + oneshot service
// unit install); version 8 added the "in" field to when_begin predicates
// (an OR-list of acceptable fact values, e.g. WhenProfile(a, b) lowering to
// a single serializable predicate instead of being marked opaque); version 9
// added the template/template_param fields to file ops (a File whose
// WithSource/path declared a ".tmpl" suffix now carries that intent onto the
// wire, so plan apply renders it on the destination instead of writing the
// raw template text — see docs/file-dir-link.md); version 10 added the
// latest field to package ops (a Package configured with IsLatest now
// carries that intent onto the wire, so plan apply runs the backend's
// upgrade-check path — dnf update / pkg upgrade / pkg_add -u / pkgin
// install — instead of a plain install).
const CurrentVersion = 10

// supportedVersions is the set of plan schema versions this binary can apply.
// Apply must refuse plans whose version is not in this set before any mutation.
var supportedVersions = map[int]struct{}{
	1:              {},
	2:              {},
	3:              {},
	4:              {},
	5:              {},
	6:              {},
	7:              {},
	8:              {},
	9:              {},
	CurrentVersion: {},
}

// SupportsVersion reports whether version is an apply-supported plan schema.
func SupportsVersion(version int) bool {
	_, ok := supportedVersions[version]
	return ok
}

// Kind is the JSONL "op" discriminator for a plan line.
type Kind string

const (
	KindPlan         Kind = "plan"
	KindLink         Kind = "link"
	KindFile         Kind = "file"
	KindDir          Kind = "dir"
	KindPackage      Kind = "package"
	KindCommand      Kind = "command"
	KindSyncDir      Kind = "sync_dir"
	KindEnsureDir    Kind = "ensure_dir"
	KindLinkIfExists Kind = "link_if_exists"
	KindWhenBegin    Kind = "when_begin"
	KindWhenEnd      Kind = "when_end"
	KindTimer        Kind = "timer"
	KindDaemonReload Kind = "daemon_reload"
	KindCron         Kind = "cron"
	KindService      Kind = "service"
	KindSystemdTimer Kind = "systemd_timer"
)

// allKinds lists every Kind constant in stable declaration order.
var allKinds = []Kind{
	KindPlan,
	KindLink,
	KindFile,
	KindDir,
	KindPackage,
	KindCommand,
	KindSyncDir,
	KindEnsureDir,
	KindLinkIfExists,
	KindWhenBegin,
	KindWhenEnd,
	KindTimer,
	KindDaemonReload,
	KindCron,
	KindService,
	KindSystemdTimer,
}

// AllKinds returns a copy of every Kind constant in stable declaration order.
// Encode/decode and tests use this as the exhaustiveness inventory.
func AllKinds() []Kind {
	out := make([]Kind, len(allKinds))
	copy(out, allKinds)
	return out
}

// IsKnownKind reports whether k is one of the declared Kind constants.
// Record-time validation and fitness tests use this to reject unknown ops
// before they reach the wire instead of failing at remote apply time.
func IsKnownKind(k Kind) bool {
	for _, known := range allKinds {
		if known == k {
			return true
		}
	}
	return false
}

// IsControlKind reports whether k is a plan-engine control op — the plan
// header or a when-block boundary — rather than a resource op. Apply sorts
// resource ops by their deps only within contiguous runs between control
// ops, so when-block bodies are never reordered across their boundaries.
func IsControlKind(k Kind) bool {
	switch k {
	case KindPlan, KindWhenBegin, KindWhenEnd:
		return true
	default:
		return false
	}
}

// Predicate is one conjunct in a when_begin "all" list.
// Exactly one of the path/fact forms should be set per predicate, and Eq/In
// are mutually exclusive alternatives for the fact form (In wins if both are
// somehow set).
type Predicate struct {
	// Fact names a host fact: "goos", "profile", or "hostname_contains".
	Fact string `json:"fact,omitempty"`
	// Eq is the expected value for Fact (equality, or substring for hostname_contains).
	Eq string `json:"eq,omitempty"`
	// In is an OR-list of acceptable values for Fact — the fact matches if
	// it equals (or, for hostname_contains, contains) any entry. Used
	// instead of Eq when a task's guard admits more than one value, e.g.
	// WhenProfile(a, b) lowers to {Fact: "profile", In: []string{a, b}}
	// rather than being treated as opaque.
	In []string `json:"in,omitempty"`
	// PathExists succeeds when the expanded path exists on the destination.
	PathExists string `json:"path_exists,omitempty"`
}

// Guard is a command probe used by unless / only_if on KindCommand lines.
type Guard struct {
	Bin          string   `json:"bin"`
	Args         []string `json:"args,omitempty"`
	ExpectStdout string   `json:"expect_stdout,omitempty"`
	// ExpectExit is the exit code that makes the probe succeed. Nil means 0.
	ExpectExit *int `json:"expect_exit,omitempty"`
}

// Op is one JSONL plan line. Fields are selected by Kind; unused fields stay zero
// and are omitted via omitempty for a stable canonical encoding.
type Op struct {
	Op      Kind   `json:"op"`
	Version int    `json:"version,omitempty"`
	ID      string `json:"id,omitempty"`

	// Path is the destination path for file/dir/link/sync/ensure ops.
	Path string `json:"path,omitempty"`
	// Symlink is the symlink target for KindLink.
	Symlink string `json:"symlink,omitempty"`
	// Target is the existence-checked path for KindLinkIfExists.
	Target string `json:"target,omitempty"`
	// Hardlink is the hardlink target when set instead of Symlink.
	Hardlink string `json:"hardlink,omitempty"`

	// Mode is an octal permission string such as "0640", "0750", or — when
	// setuid/setgid/sticky are set — four digits like "04755", for path
	// metadata.
	Mode string `json:"mode,omitempty"`
	// FileMode is an octal permission string applied to files copied by KindSyncDir
	// (same format as Mode, including the four-digit special-bit form).
	FileMode string `json:"file_mode,omitempty"`
	// Owner is the owning user (name or numeric uid) recorded for the
	// filesystem ops KindFile, KindDir, KindSyncDir, and KindEnsureDir. It is
	// only set when the task explicitly configured ownership via WithOwner;
	// empty means the destination apply leaves the owner as-is.
	Owner string `json:"owner,omitempty"`
	// Group is the owning group (name or numeric gid) recorded for the
	// filesystem ops KindFile, KindDir, KindSyncDir, and KindEnsureDir. It is
	// only set when the task explicitly configured ownership via WithGroup;
	// empty means the destination apply leaves the group as-is.
	Group string `json:"group,omitempty"`
	// ContentB64 is base64 file content for KindFile (InstallFile-style).
	// A legitimately empty file (WithContent("") or an empty WithSource
	// file) also base64-encodes to "", so this alone cannot tell "empty
	// content" apart from "no content recorded"; see HasContent.
	ContentB64 string `json:"content_b64,omitempty"`
	// Blob is a sidecar blob id/path for KindSyncDir (or large KindFile content).
	Blob string `json:"blob,omitempty"`
	// HasContent marks that KindFile's content was explicitly configured
	// (WithContent or WithSource), even when it resolves to zero bytes and
	// ContentB64 is therefore "". Apply uses it to accept a legitimately
	// empty file while still erroring loudly when both ContentB64 and Blob
	// are unset AND HasContent is false (a record-time bug).
	HasContent bool `json:"has_content,omitempty"`
	// Template marks that KindFile's content must be rendered as a
	// text/template on the destination (schema v9): the recipe's source or
	// destination path ended in ".tmpl" at record time. By apply time the
	// content already travels as raw template text in ContentB64/Blob and
	// neither Path nor an (empty, wire content is never re-sourced) source
	// path still carries the ".tmpl" suffix that would otherwise trigger
	// rendering, so this flag is what carries the intent across the wire.
	Template bool `json:"template,omitempty"`
	// TemplateParam is the recipe's declared source path, recorded alongside
	// Template so the destination render uses the same {{.Param}} default a
	// direct (non-plan) File with the same ".tmpl" source would use, instead
	// of exposing the plan-apply implementation detail (there is no source
	// file on the destination to derive it from).
	TemplateParam string `json:"template_param,omitempty"`
	// SourceDir is the recipe's declared source directory for KindSyncDir
	// (for the glob flavor, the declared glob pattern's directory). Apply
	// passes it to the synced tree so .tmpl files inside render {{.Param}}
	// from the stable declared identity ("source_dir/relative entry path")
	// instead of the ephemeral blob-extraction path, which changes every
	// plan run. Empty on plans recorded before schema v6: apply then keeps
	// the blob-path Param (pre-v6 behavior).
	SourceDir string `json:"source_dir,omitempty"`
	// Prune removes destination entries not present in the sync source.
	Prune bool `json:"prune,omitempty"`
	// Absent marks NoFile / NoDir / NoLink / NoPackage style removal.
	Absent bool `json:"absent,omitempty"`
	// Latest marks KindPackage as configured with IsLatest (schema v10):
	// destination apply must run the backend's upgrade-check path (dnf
	// update / pkg upgrade / pkg_add -u / pkgin install) instead of a plain
	// install, even when the package is already present.
	Latest bool `json:"latest,omitempty"`

	// AddLine appends a line to a file when missing (line-in-file).
	AddLine string `json:"add_line,omitempty"`
	// RemoveLine removes matching lines from a file.
	RemoveLine string `json:"remove_line,omitempty"`

	// Name is a package name, command registry name, or similar label.
	Name string `json:"name,omitempty"`
	// Bin is the executable for KindCommand.
	Bin string `json:"bin,omitempty"`
	// Args are argv after Bin for KindCommand.
	Args []string `json:"args,omitempty"`
	// Dir is the working directory for KindCommand.
	Dir string `json:"dir,omitempty"`
	// Env is extra environment for KindCommand.
	Env map[string]string `json:"env,omitempty"`
	// Creates skips KindCommand when this path already exists.
	Creates string `json:"creates,omitempty"`
	// Unless skips KindCommand when the guard probe succeeds.
	Unless *Guard `json:"unless,omitempty"`
	// OnlyIf runs KindCommand only when the guard probe succeeds.
	OnlyIf *Guard `json:"only_if,omitempty"`

	// CronUser is the crontab owner for KindCron (default root).
	CronUser string `json:"cron_user,omitempty"`
	// Command is the crontab command for KindCron.
	Command string `json:"command,omitempty"`
	// Schedule holds the five space-separated cron time fields
	// (minute hour monthday month weekday) for KindCron.
	Schedule string `json:"schedule,omitempty"`
	// CronEnv lists KEY=VAL environment lines above the KindCron job.
	CronEnv []string `json:"cron_env,omitempty"`

	// OnCalendar is the systemd OnCalendar= expression for KindSystemdTimer.
	OnCalendar string `json:"on_calendar,omitempty"`
	// OnBootSec is the systemd OnBootSec= delay for KindSystemdTimer.
	OnBootSec string `json:"on_boot_sec,omitempty"`
	// Persistent sets Persistent=true on KindSystemdTimer units.
	Persistent bool `json:"persistent,omitempty"`
	// Description is the [Unit] Description for KindSystemdTimer.
	Description string `json:"description,omitempty"`
	// ServiceDescription is the companion oneshot .service Description.
	ServiceDescription string `json:"service_description,omitempty"`
	// After lists After= dependencies on the companion oneshot .service.
	After []string `json:"after,omitempty"`
	// Wants lists Wants= dependencies on the companion oneshot .service.
	Wants []string `json:"wants,omitempty"`

	// User selects systemd --user for KindTimer / KindDaemonReload / KindService / KindSystemdTimer.
	User bool `json:"user,omitempty"`
	// Restart restarts KindService / KindTimer / KindSystemdTimer once when
	// it is already running.
	Restart bool `json:"restart,omitempty"`
	// Reload reloads KindService once when it is already running
	// (no restart fallback).
	Reload bool `json:"reload,omitempty"`
	// EnableOnly skips start/stop for KindTimer / KindSystemdTimer present
	// (enable/disable only).
	EnableOnly bool `json:"enable_only,omitempty"`
	// IfChanged gates KindDaemonReload on watched dependency outcomes.
	IfChanged bool `json:"if_changed,omitempty"`
	// Watch lists resource ids consulted when IfChanged is set.
	Watch []string `json:"watch,omitempty"`

	// Elevate marks ops from a Privileged() task (or WithElevate command).
	// Controllers use this to split apply into user vs sudo/doas gonf invocations.
	Elevate bool `json:"elevate,omitempty"`

	// Deps lists the resource IDs (op IDs such as "File[/etc/foo]") this op
	// depends on, recorded from the resource DependsOn option. Plan apply
	// topologically sorts resource ops by deps within each contiguous run
	// between control ops, mirroring the repository path; ops are never
	// reordered across when_* boundaries.
	Deps []string `json:"deps,omitempty"`

	// All is the conjunctive predicate list for KindWhenBegin.
	All []Predicate `json:"all,omitempty"`
}
