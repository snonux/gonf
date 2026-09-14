// Package plan defines the versioned JSONL wire types and codec for remote
// gonf plan/apply (encode/decode, version gate).
//
// See the overall design plan (Remote gonf — plan/apply with serialized JSONL).
package plan

// CurrentVersion is the plan wire schema version emitted by gonf plan.
// Version 2 added timer and daemon_reload ops; version 3 added cron and
// service ops; version 4 added owner/group fields to the filesystem ops
// (file, dir, sync_dir, ensure_dir).
const CurrentVersion = 4

// supportedVersions is the set of plan schema versions this binary can apply.
// Apply must refuse plans whose version is not in this set before any mutation.
var supportedVersions = map[int]struct{}{
	1:              {},
	2:              {},
	3:              {},
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

// Predicate is one conjunct in a when_begin "all" list.
// Exactly one of the path/fact forms should be set per predicate.
type Predicate struct {
	// Fact names a host fact: "goos", "profile", or "hostname_contains".
	Fact string `json:"fact,omitempty"`
	// Eq is the expected value for Fact (equality, or substring for hostname_contains).
	Eq string `json:"eq,omitempty"`
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

	// Mode is an octal permission string such as "0640" or "0750" for path metadata.
	Mode string `json:"mode,omitempty"`
	// FileMode is an octal permission string applied to files copied by KindSyncDir.
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
	ContentB64 string `json:"content_b64,omitempty"`
	// Blob is a sidecar blob id/path for KindSyncDir (or large KindFile content).
	Blob string `json:"blob,omitempty"`
	// Prune removes destination entries not present in the sync source.
	Prune bool `json:"prune,omitempty"`
	// Absent marks NoFile / NoDir / NoLink / NoPackage style removal.
	Absent bool `json:"absent,omitempty"`

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

	// User selects systemd --user for KindTimer / KindDaemonReload / KindService.
	User bool `json:"user,omitempty"`
	// Restart restarts KindService / KindTimer once when it is already
	// running.
	Restart bool `json:"restart,omitempty"`
	// Reload reloads KindService once when it is already running
	// (no restart fallback).
	Reload bool `json:"reload,omitempty"`
	// EnableOnly skips start/stop for KindTimer present (enable/disable only).
	EnableOnly bool `json:"enable_only,omitempty"`
	// IfChanged gates KindDaemonReload on watched dependency outcomes.
	IfChanged bool `json:"if_changed,omitempty"`
	// Watch lists resource ids consulted when IfChanged is set.
	Watch []string `json:"watch,omitempty"`

	// Elevate marks ops from a Privileged() task (or WithElevate command).
	// Controllers use this to split apply into user vs sudo/doas gonf invocations.
	Elevate bool `json:"elevate,omitempty"`

	// All is the conjunctive predicate list for KindWhenBegin.
	All []Predicate `json:"all,omitempty"`
}
