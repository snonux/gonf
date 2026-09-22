// Package plan defines the versioned JSONL wire types and codec for remote
// gonf plan/apply (encode/decode, version gate).
//
// See the overall design plan (Remote gonf — plan/apply with serialized JSONL).
package plan

import "encoding/json"

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
// install — instead of a plain install); version 11 extended the
// if_changed/watch fields (previously daemon_reload-only) to command,
// service, and timer ops: the OnChange option arms a change gate on those
// resources (skip the command; hold restart/reload until a watched resource
// changed), and an older binary that ignored the fields on the new kinds
// would run the command / restart the service unconditionally every apply —
// the same intent-loss class as previous bumps, so v10 binaries refuse v11
// plans up-front at the header gate instead, while this binary keeps
// applying v1–10 plans. Version 12 adds template_data to file ops for
// structured destination-side template rendering. Version 13 adds
// additive-only user operations and their creation-time account attributes.
// Version 14 adds ensure_file plus ordered batched file line edits. Version
// 15 extends Env from command operations to package operations, so older
// destination binaries reject rather than silently ignore WithEnv. Version
// 16 lets file operations carry an explicit Name identity distinct from Path:
// an older destination would ignore that identity, report changes under
// File[path], and make an OnChange target of File[name] silently never fire.
// It must therefore refuse v16 before any mutation. Version 17 adds
// legacy_command to cron operations. An older destination would leave the
// unmanaged legacy line in place and run it alongside the new managed block,
// so it must refuse v17 before any mutation. Version 18 adds file validator
// argv fields; older binaries would otherwise publish unvalidated content.
// Version 19 adds manage_home to user operations: an older destination would
// ignore it and silently leave an existing account's home field unmanaged
// while reporting success, so it must refuse v19 at the header gate instead.
// Version 20 adds require to when_begin: a failed predicate then refuses the
// whole apply (and dry run) instead of skipping the body. An older binary
// would silently skip a required block, so it must refuse v20 up-front.
// Version 21 adds the config_set and config_set_member kinds and their
// members/validators/chroot/staging_dir/member fields: an older destination
// would reach the unknown kind mid-plan, after earlier ops had already
// mutated the host, so it must refuse v21 at the header gate instead.
// Version 22 adds the sensitive field: an op whose payload holds secret
// material (see VersionSensitive). An older destination would ignore it and
// echo a failing validator's output — possibly the secret — into its error,
// so it must refuse v22 at the header gate; push then installs a current
// gonf first and strict preview refuses (remote.RequireRemoteGonf). A
// recorded plan declares v22 only when it has a sensitive op
// (RequiredVersion); otherwise its header stays v21.
// Version 23 adds keyed_lines to file operations (WithKeyedLine, see
// VersionKeyedLines). An older destination would ignore them: it would
// leave a legacy or conflicting line in place, or skip a keyed-only edit
// entirely, while reporting success, so it must refuse v23 at the header
// gate. Like v22 it is declared on demand: only a plan with a keyed line
// edit needs v23.
const CurrentVersion = 23

// VersionKeyedLines is the plan schema version that introduced the file op
// keyed_lines field. Tests pin it so a merge that loses the bump (and so
// lets an older destination silently ignore a keyed edit) fails loudly.
const VersionKeyedLines = 23

// VersionSensitive is the plan schema version that introduced the op
// sensitive field. Tests pin it so a merge that loses the bump (and so lets
// an older destination apply a secret-bearing op without honouring it)
// fails loudly.
const VersionSensitive = 22

// VersionConfigSet is the plan schema version that introduced the config_set
// and config_set_member kinds. Tests pin it so a merge that loses the bump
// fails loudly instead of letting older destinations fail mid-apply.
const VersionConfigSet = 21

// VersionUserManageHome is the plan schema version that introduced the user
// op's manage_home field. Tests pin it so a merge that loses the bump (and so
// lets an older destination silently ignore manage_home) fails loudly.
const VersionUserManageHome = 19

// VersionWhenRequire is the plan schema version that introduced when_begin's
// require field (the LoginClass OpenBSD requirement). Pinned by tests for the
// same reason as VersionUserManageHome.
const VersionWhenRequire = 20

// KeyedLine is one file-op keyed line edit on the wire: Line owns the one
// line of the file starting with the literal prefix Key.
type KeyedLine struct {
	Key  string `json:"key"`
	Line string `json:"line"`
}

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
	10:             {},
	11:             {},
	12:             {},
	13:             {},
	14:             {},
	15:             {},
	16:             {},
	17:             {},
	18:             {},
	19:             {},
	20:             {},
	21:             {},
	22:             {},
	CurrentVersion: {},
}

// SupportsVersion reports whether version is an apply-supported plan schema.
func SupportsVersion(version int) bool {
	_, ok := supportedVersions[version]
	return ok
}

// Kind is the JSONL "op" discriminator for a plan line.
type Kind string

// Plan op kinds. KindPlan is the header line every plan starts with; every
// other kind is one resource or control op (KindWhenBegin/KindWhenEnd bracket
// a conditional block). Each kind must also be listed in allKinds.
const (
	KindPlan         Kind = "plan"
	KindLink         Kind = "link"
	KindFile         Kind = "file"
	KindDir          Kind = "dir"
	KindPackage      Kind = "package"
	KindCommand      Kind = "command"
	KindSyncDir      Kind = "sync_dir"
	KindEnsureDir    Kind = "ensure_dir"
	KindEnsureFile   Kind = "ensure_file"
	KindLinkIfExists Kind = "link_if_exists"
	KindWhenBegin    Kind = "when_begin"
	KindWhenEnd      Kind = "when_end"
	KindTimer        Kind = "timer"
	KindDaemonReload Kind = "daemon_reload"
	KindCron         Kind = "cron"
	KindService      Kind = "service"
	KindSystemdTimer Kind = "systemd_timer"
	KindUser         Kind = "user"
	// KindConfigSet validates a complete staged multi-file configuration and
	// then publishes its members (see resource/configset).
	KindConfigSet Kind = "config_set"
	// KindConfigSetMember is a report-only handle for one config_set member:
	// it gives the member its own op ID so OnChange can watch it.
	KindConfigSetMember Kind = "config_set_member"
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
	KindEnsureFile,
	KindLinkIfExists,
	KindWhenBegin,
	KindWhenEnd,
	KindTimer,
	KindDaemonReload,
	KindCron,
	KindService,
	KindSystemdTimer,
	KindUser,
	KindConfigSet,
	KindConfigSetMember,
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
	// TemplateData is JSON-compatible data supplied by WithTemplateData.
	TemplateData json.RawMessage `json:"template_data,omitempty"`
	// ValidationBin and ValidationArgs are an optional file validator argv.
	// ValidationArgs contains CandidatePath, which destination apply replaces
	// with a private staged filename before starting ValidationBin.
	ValidationBin  string   `json:"validation_bin,omitempty"`
	ValidationArgs []string `json:"validation_args,omitempty"`
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

	// PrimaryGroup and SupplementaryGroups are the requested groups for a
	// KindUser operation. Only missing supplementary memberships are added;
	// no existing membership or primary group is removed or rewritten.
	PrimaryGroup        string   `json:"primary_group,omitempty"`
	SupplementaryGroups []string `json:"supplementary_groups,omitempty"`
	// Home, CreateHome, Shell, LoginClass, and System are only used when a
	// KindUser operation creates a missing account; Home is additionally the
	// target of an existing account's home field when ManageHome is set.
	Home       string `json:"home,omitempty"`
	CreateHome bool   `json:"create_home,omitempty"`
	Shell      string `json:"shell,omitempty"`
	LoginClass string `json:"login_class,omitempty"`
	System     bool   `json:"system,omitempty"`
	// ManageHome (schema v19, VersionUserManageHome) opts a KindUser
	// operation in to converging an existing account's passwd home field to
	// Home. It never moves, creates, or chowns the directory.
	ManageHome bool `json:"manage_home,omitempty"`

	// AddLines appends lines to a file when missing (line-in-file), in order.
	AddLines []string `json:"add_lines,omitempty"`
	// RemoveLines removes matching lines from a file, in order.
	RemoveLines []string `json:"remove_lines,omitempty"`
	// KeyedLines (schema v23, VersionKeyedLines) are WithKeyedLine edits,
	// applied after RemoveLines and before AddLines: each replaces the first
	// line starting with its key in place, drops every further one, and is
	// appended when none exists. An older destination would ignore the field
	// and leave the legacy line (or skip the whole edit), so it must refuse
	// v23 at the header gate.
	KeyedLines []KeyedLine `json:"keyed_lines,omitempty"`
	// AddLine and RemoveLine are accepted when applying pre-v14 plans. Current
	// recording never sets them (resource.PlanDraft has no singular fields);
	// they stay on the wire type only so old recorded plans decode and apply.
	AddLine    string `json:"add_line,omitempty"`
	RemoveLine string `json:"remove_line,omitempty"`

	// Name is a package name, command registry name, file resource identity,
	// or similar label. For a named KindFile it keeps the resource ID stable
	// independently of Path, which remains the destination to manage.
	Name string `json:"name,omitempty"`
	// Bin is the executable for KindCommand.
	Bin string `json:"bin,omitempty"`
	// Args are argv after Bin for KindCommand.
	Args []string `json:"args,omitempty"`
	// Dir is the working directory for KindCommand.
	Dir string `json:"dir,omitempty"`
	// Env is extra environment for KindCommand and KindPackage.
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
	// LegacyCommand opts KindCron into adopting one exact unmanaged command.
	LegacyCommand string `json:"legacy_command,omitempty"`
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
	// IfChanged gates the op's mutating action on watched dependency
	// outcomes (schema v2 for KindDaemonReload; schema v11 extended the
	// field to KindCommand, KindService, and KindTimer): a gated command is
	// skipped entirely, a service/timer's restart/reload action is held,
	// and a daemon-reload is skipped, unless one of the Watch ids reported
	// a change during this apply. Change reports are chunk-local: the
	// controller-side ValidateChangeGates pre-flight refuses gated ops
	// whose watch ids are recorded in another privilege chunk.
	IfChanged bool `json:"if_changed,omitempty"`
	// Watch lists resource ids consulted when IfChanged is set.
	Watch []string `json:"watch,omitempty"`

	// Sensitive (schema v22, VersionSensitive) marks an op whose payload —
	// content_b64 or its blob, template_data, member content, lines, argv,
	// environment — holds secret material. The controller sets it while
	// recording, when the op contains a value resolved through the secret
	// provider (api.ResolveSecret, MustSecret, OptionalSecret, SecretFile),
	// or when the recipe declared the payload secret with
	// options.WithSensitive (transformed values, synced trees).
	// It is not protection by itself: the payload stays in clear text (base64
	// is an encoding, not encryption). What it changes: `gonf plan -stdout`
	// refuses the plan unless explicitly asked, the redacted preview
	// withholds every payload string of the op, a failing file or config_set
	// validator's output and a file's (or synced tree entry's) template
	// error details are withheld, a command's argv is withheld from its log
	// lines and dry-run description and its output from its failure, a
	// failing package-manager run reports only its output sizes (a failing
	// crontab run does so for every cron op), and a push refuses to stage
	// the op's blob where a less privileged user could read it. It does not
	// hide argv from the destination's process list, or content the op
	// writes (a crontab line, a file). Recording refuses a strong secret
	// (8+ bytes, not word-like) in the op's identity; a weak one there only
	// marks the op and stays visible in destination logs.
	Sensitive bool `json:"sensitive,omitempty"`

	// Elevate marks ops from a Privileged() task (or WithElevate command).
	// Controllers use this to split apply into user vs sudo/doas gonf invocations.
	Elevate bool `json:"elevate,omitempty"`

	// Members are the files of a KindConfigSet op, in declaration order
	// (schema v21). Content may reference other members' staged or live
	// paths through gonf-owned tokens; see ConfigMember.
	Members []ConfigMember `json:"members,omitempty"`
	// Validators are the argv commands a KindConfigSet op runs, in order,
	// against the complete staged candidate set before any live write.
	Validators []Argv `json:"validators,omitempty"`
	// Chroot is the optional chroot directory every KindConfigSet member and
	// its staging directory must live under; chroot-relative member tokens
	// are rendered relative to it.
	Chroot string `json:"chroot,omitempty"`
	// StagingDir is the KindConfigSet directory that receives the private
	// staging directory. Empty means the members' deepest common directory.
	StagingDir string `json:"staging_dir,omitempty"`
	// Member is the member key of a KindConfigSetMember op; its Name is the
	// owning set's name.
	Member string `json:"member,omitempty"`

	// Deps lists the resource IDs (op IDs such as "File[/etc/foo]") this op
	// depends on, recorded from the resource DependsOn option. Plan apply
	// topologically sorts resource ops by deps within each contiguous run
	// between control ops; ops are never reordered across when_*
	// boundaries.
	Deps []string `json:"deps,omitempty"`

	// All is the conjunctive predicate list for KindWhenBegin.
	All []Predicate `json:"all,omitempty"`
	// Require, on KindWhenBegin, turns the block into a requirement: when
	// the block's enclosing scope is active but All does not hold, Apply
	// refuses with this human-readable requirement (plus the host GOOS)
	// instead of skipping the body. A requirement's own predicates and every
	// when_begin enclosing it must be host facts (goos, profile,
	// hostname_contains); a requirement under path_exists or any other
	// condition is refused at record time (ValidateChunks) and by Apply's
	// pre-check. Because host facts cannot change during an apply, Apply
	// decides every requirement before the first mutation, so a refused plan
	// — dry run included — writes and predicts nothing. Schema version 20.
	Require string `json:"require,omitempty"`
}
