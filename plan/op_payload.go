package plan

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
)

// OpPayload holds the wire fields exclusive to one Op's Kind (task yd2,
// "Layer 2" of the PlanDraft/Op god-struct split — see docs/design/plan.md, "The
// PlanDraft/Op split", and resource.DraftPayload's identical Layer 1 role
// for resource.PlanDraft). Decode must build one without importing a
// resource/<kind> package (plan/layering_test.go's TestPlanImportsOnlyResourceCore
// pins that plan never imports one), so every concrete OpPayload is
// declared in this package; applyToWire is unexported so only a type
// declared here can implement it — the same closed-set discipline
// TestSourcePayloadFitness (api/plan_fitness_test.go) already enforces for
// resource.DraftPayload's marker interfaces.
type OpPayload interface {
	// applyToWire copies this payload's fields onto w, so Op.MarshalJSON's
	// merged wireOp carries them at the same wire position they always had.
	applyToWire(w *wireOp)
}

// PayloadOf type-asserts op.Payload to T, returning the zero T instead of
// failing when it is absent or holds some other concrete type (task rf2,
// replacing 16 copy-pasted comma-ok assertions across 10 call sites that
// had drifted out of sync — one of them, plan/sensitive.go's RequiredVersion,
// was already wrong: see below).
//
// This is deliberately NOT a "missing payload" error. Op.Payload is trusted
// (a resource package's own ToOp is fed only draft data its own planDraft
// always populates), but a caller reading an arbitrary plan.Op is not:
// Apply may see an op decoded from an arbitrary plan.jsonl, and nothing on
// the record path rules out an in-process Op built with a Payload of the
// wrong concrete type for its own Kind (see plan/sensitive.go's
// TestRequiredVersion "glob on non-sync_dir op" case, task pf2 — a mutation
// probe found RequiredVersion's own Kind guard, without that case, could be
// deleted with every test still green). The zero T then reads as "every
// T-exclusive field unset," which each caller's own downstream check
// already turns into either a clean "missing X" error (a present op that
// needed the field) or a harmless no-op default — never a panic.
//
// PayloadOf alone cannot tell "legitimately absent" apart from "wrong Kind
// entirely": a caller that must not conflate the two (RequiredVersion is
// the one example so far) still guards on op.Op itself before trusting the
// result, the same way it always had to.
func PayloadOf[T OpPayload](op Op) T {
	p, _ := op.Payload.(T)
	return p
}

// CronPayload holds the wire fields exclusive to KindCron. resource/cron's
// planwire.go is the only other package that constructs or reads one — it
// always sets a non-nil CronPayload on a "cron" op's Payload (record side:
// ToOp; apply side: Apply type-asserts it), so a decoded or freshly lowered
// KindCron op's Payload is never nil, keeping encode/decode round trips
// symmetric (see payloadFromWire).
//
// Its json tags are never consulted by encoding/json — Op.MarshalJSON
// merges these fields onto a wireOp and marshals THAT (applyToWire below),
// never this struct directly — but api's secret-scan reflection walkers
// (scanOpStrings/redactOpStrings, opFieldClasses) still need them: it
// descends into Op.Payload's concrete value at the op's own top-level path
// (see api/secret_fields.go's scanStruct/redactStruct), and computes each
// leaf's classification path from THESE tags. They must therefore keep naming the
// same wire keys wireOp's own fields do; TestWirePayloadTagsMatch
// (types_test.go) pins that the two never drift apart.
//
// Field docs (unchanged from Op's pre-yd2 flat field comments):
type CronPayload struct {
	// CronUser is the crontab owner for KindCron (default root).
	CronUser string `json:"cron_user,omitempty"`
	// LegacyCommand opts KindCron into adopting one exact unmanaged command.
	LegacyCommand string `json:"legacy_command,omitempty"`
	// Schedule holds the five space-separated cron time fields
	// (minute hour monthday month weekday) for KindCron.
	Schedule string `json:"schedule,omitempty"`
	// CronEnv lists KEY=VAL environment lines above the KindCron job.
	CronEnv []string `json:"cron_env,omitempty"`
}

func (p CronPayload) applyToWire(w *wireOp) {
	w.CronUser = p.CronUser
	w.LegacyCommand = p.LegacyCommand
	w.Schedule = p.Schedule
	w.CronEnv = p.CronEnv
}

// SystemdTimerPayload holds the wire fields exclusive to KindSystemdTimer.
// resource/systemdtimer's planwire.go is the only other package that
// constructs or reads one, always non-nil on a "systemd_timer" op's
// Payload, for the same reason CronPayload is (see its doc comment). Its
// json tags exist for the same secret-scan reflection reason CronPayload's
// do — see CronPayload's doc comment.
type SystemdTimerPayload struct {
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
}

func (p SystemdTimerPayload) applyToWire(w *wireOp) {
	w.OnCalendar = p.OnCalendar
	w.OnBootSec = p.OnBootSec
	w.Persistent = p.Persistent
	w.Description = p.Description
	w.ServiceDescription = p.ServiceDescription
	w.After = p.After
	w.Wants = p.Wants
}

// UserPayload holds the wire fields exclusive to KindUser. resource/user's
// planwire.go is the only other package that constructs or reads one — it
// always sets a non-nil UserPayload on a "user" op's Payload (record side:
// draftOp; apply side: opOptions type-asserts it), so a decoded or freshly
// lowered KindUser op's Payload is never nil, keeping encode/decode round
// trips symmetric (see payloadFromWire).
//
// Its json tags are never consulted by encoding/json — Op.MarshalJSON
// merges these fields onto a wireOp and marshals THAT (applyToWire below),
// never this struct directly — but api's secret-scan reflection walkers
// (scanOpStrings/redactOpStrings, opFieldClasses) still need them: it
// descends into Op.Payload's concrete value at the op's own top-level path
// (see api/secret_fields.go's scanStruct/redactStruct), and computes each
// leaf's classification path from THESE tags. They must therefore keep naming the
// same wire keys wireOp's own fields do; TestWirePayloadTagsMatch
// (types_test.go) pins that the two never drift apart.
//
// Field docs (unchanged from Op's pre-yd2/6e2 flat field comments):
type UserPayload struct {
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
}

func (p UserPayload) applyToWire(w *wireOp) {
	w.PrimaryGroup = p.PrimaryGroup
	w.SupplementaryGroups = p.SupplementaryGroups
	w.Home = p.Home
	w.CreateHome = p.CreateHome
	w.Shell = p.Shell
	w.LoginClass = p.LoginClass
	w.System = p.System
	w.ManageHome = p.ManageHome
}

// LinkPayload holds the wire fields exclusive to KindLink.
// resource/link's planwire.go is the only other package that constructs or
// reads one (its ToOp/Apply for the "link" kind specifically —
// LinkIfExistsPayload below is the separate payload for the "link_if_exists"
// kind that same file also handles), always non-nil on a "link" op's
// Payload, for the same reason CronPayload is (see its doc comment). Its
// json tags exist for the same secret-scan reflection reason CronPayload's
// do — see CronPayload's doc comment.
type LinkPayload struct {
	// Symlink is the symlink target.
	Symlink string `json:"symlink,omitempty"`
	// Hardlink is the hardlink target, set instead of Symlink.
	Hardlink string `json:"hardlink,omitempty"`
}

func (p LinkPayload) applyToWire(w *wireOp) {
	w.Symlink = p.Symlink
	w.Hardlink = p.Hardlink
}

// LinkIfExistsPayload holds the wire field exclusive to KindLinkIfExists.
// resource/link's planwire.go is the only other package that constructs or
// reads one, always non-nil on a "link_if_exists" op's Payload, for the same
// reason CronPayload is (see its doc comment).
type LinkIfExistsPayload struct {
	// Target is the existence-checked path.
	Target string `json:"target,omitempty"`
}

func (p LinkIfExistsPayload) applyToWire(w *wireOp) {
	w.Target = p.Target
}

// PackagePayload holds the wire field exclusive to KindPackage.
// resource/pkg's planwire.go is the only other package that constructs or
// reads one, always non-nil on a "package" op's Payload, for the same
// reason CronPayload is (see its doc comment).
type PackagePayload struct {
	// Latest marks a KindPackage op configured with IsLatest: destination
	// apply must run the backend's upgrade-check path (dnf update / pkg
	// upgrade / pkg_add -u / pkgin install) instead of a plain install, even
	// when the package is already present.
	Latest bool `json:"latest,omitempty"`
}

func (p PackagePayload) applyToWire(w *wireOp) {
	w.Latest = p.Latest
}

// CommandPayload holds the wire fields exclusive to KindCommand (task 7e2,
// Layer 2's third slice). resource/cmd's planwire.go is the only other
// package that constructs or reads one, always non-nil on a "command" op's
// Payload, for the same reason CronPayload is (see its doc comment). Its
// json tags exist for the same secret-scan reflection reason CronPayload's
// do — see CronPayload's doc comment.
//
// Unless/OnlyIf are *Guard pointers that MAY be shared with other Op values
// referencing the same underlying Guard (e.g. every per-host goroutine in a
// fleet/cluster push encodes its own copy of the same ops slice — see
// internal/remote/fleet.go Fanout and TestEncodePlanConcurrentSharedGuardNoRace,
// plan/codec_test.go). applyToWire below therefore only ever copies the
// POINTER VALUE onto wireOp — it must never mutate *p.Unless/*p.OnlyIf in
// place. wireOp's own normalizeWire (wire.go) does the actual
// copy-before-mutate normalization, on ITS copy of the pointer, exactly as
// it already does for Op.Unless/Op.OnlyIf pre-yd2; moving these fields onto
// a payload changes nothing about that contract, since applyToWire runs
// strictly before normalizeWire in both MarshalJSON and toWire's caller.
type CommandPayload struct {
	// Bin is the executable for KindCommand.
	Bin string `json:"bin,omitempty"`
	// Args are argv after Bin for KindCommand.
	Args []string `json:"args,omitempty"`
	// Dir is the working directory for KindCommand.
	Dir string `json:"dir,omitempty"`
	// Creates skips KindCommand when this path already exists.
	Creates string `json:"creates,omitempty"`
	// Unless skips KindCommand when the guard probe succeeds.
	Unless *Guard `json:"unless,omitempty"`
	// OnlyIf runs KindCommand only when the guard probe succeeds.
	OnlyIf *Guard `json:"only_if,omitempty"`
}

func (p CommandPayload) applyToWire(w *wireOp) {
	w.Bin = p.Bin
	w.Args = p.Args
	w.Dir = p.Dir
	w.Creates = p.Creates
	// Pointer-value copy only — see the type doc comment above for why this
	// must not become *w.Unless = *p.Unless or any other in-place write.
	w.Unless = p.Unless
	w.OnlyIf = p.OnlyIf
}

// ConfigSetPayload holds the wire fields exclusive to KindConfigSet (task
// 8e2). resource/configset's planwire.go is the only other package that
// constructs or reads one — its setHandler always sets a non-nil
// ConfigSetPayload on a "config_set" op's Payload (record side: ToOp; apply
// side: specFromOp comma-ok type-asserts it, degrading to the zero value for
// an op decoded from an arbitrary plan.jsonl, the same contract
// CronPayload's doc comment describes), so a decoded or freshly lowered
// KindConfigSet op's Payload is never nil in the normal path.
//
// This is the plan.Op ("Layer 2") counterpart of resource/configset's own
// SetPayload ("Layer 1", resource/configset/payload.go): both split the same
// two-kinds-one-package shape (KindConfigSet/KindConfigSetMember), one on
// the draft side and one on the wire side.
//
// Its json tags are never consulted by encoding/json — Op.MarshalJSON merges
// these fields onto a wireOp and marshals THAT, never this struct directly —
// but api's secret-scan reflection walkers (scanOpStrings/redactOpStrings,
// opFieldClasses) still need them: it descends into Op.Payload's concrete
// value at the op's own top-level path (see api/secret_fields.go's
// scanStruct/redactStruct), and computes each leaf's classification path
// from THESE tags. They must
// therefore keep naming the same wire keys wireOp's own fields do;
// TestWirePayloadTagsMatch (types_test.go) pins that the two never drift
// apart.
//
// Field docs (unchanged from Op's pre-8e2 flat field comments):
type ConfigSetPayload struct {
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
}

func (p ConfigSetPayload) applyToWire(w *wireOp) {
	w.Members = p.Members
	w.Validators = p.Validators
	w.Chroot = p.Chroot
	w.StagingDir = p.StagingDir
}

// ConfigSetMemberPayload holds the wire field exclusive to
// KindConfigSetMember (task 8e2). resource/configset's planwire.go is the
// only other package that constructs or reads one, always non-nil on a
// "config_set_member" op's Payload in the normal path, for the same reason
// ConfigSetPayload's doc comment gives.
//
// This is the plan.Op ("Layer 2") counterpart of resource/configset's own
// MemberPayload ("Layer 1", resource/configset/payload.go) — see
// ConfigSetPayload's doc comment above for the shared two-kinds-one-package
// shape both split.
type ConfigSetMemberPayload struct {
	// Member is the member key of a KindConfigSetMember op; the op's Name is
	// the owning set's name.
	Member string `json:"member,omitempty"`
}

func (p ConfigSetMemberPayload) applyToWire(w *wireOp) {
	w.Member = p.Member
}

// SyncDirPayload holds the wire fields exclusive to KindSyncDir (task 9e2,
// Layer 2's fifth slice). resource/dir's planwire.go is the only other
// non-test package that constructs or reads one — it always sets a
// non-nil SyncDirPayload on a "sync_dir" op's Payload (record side:
// syncDirHandler.ToOp; apply side: syncDirHandler.Apply/syncDirOptions/
// syncDirSourceOption all comma-ok assert it), so a decoded or freshly
// lowered KindSyncDir op's Payload is never nil, keeping encode/decode
// round trips symmetric (see payloadFromWire). Prune is NOT here despite
// reading as sync_dir-specific: KindDir genuinely shares it with identical
// meaning, so it stays a flat Op core field — see Op.Prune's own doc
// comment (types.go) for the full rationale, verified by grepping every
// Op{...Prune...} literal and op.Prune read in the repo before this task
// assumed otherwise (the same check w62's Layer 1 SyncPayload, resource/
// dir/payload.go, already did for resource.PlanDraft.Prune).
//
// Its json tags are never consulted by encoding/json — Op.MarshalJSON
// merges these fields onto a wireOp and marshals THAT (applyToWire below),
// never this struct directly — but api's secret-scan reflection walkers
// (scanOpStrings/redactOpStrings, opFieldClasses) still need them: it
// descends into Op.Payload's concrete value at the op's own top-level path
// (see api/secret_fields.go's scanStruct/redactStruct), and computes each
// leaf's classification path from THESE tags. They must therefore keep naming the
// same wire keys wireOp's own fields do; TestWirePayloadTagsMatch
// (types_test.go) pins that the two never drift apart.
//
// Field docs (unchanged from Op's pre-9e2 flat field comments):
type SyncDirPayload struct {
	// SourceDir is the recipe's declared source directory for KindSyncDir
	// (for the glob flavor, the declared glob pattern's directory). Apply
	// passes it to the synced tree so .tmpl files inside render {{.Param}}
	// from the stable declared identity ("source_dir/relative entry path")
	// instead of the ephemeral blob-extraction path, which changes every
	// plan run. Empty on plans recorded before schema v6: apply then keeps
	// the blob-path Param (pre-v6 behavior).
	SourceDir string `json:"source_dir,omitempty"`
	// Glob (schema v24, VersionSyncDirGlob) marks a KindSyncDir op recorded
	// from WithSourceGlob: its blob is the flat set of counting glob
	// matches, not a tree. Apply then installs the blob's entries by
	// basename and, with the core Op.Prune, removes only regular files
	// directly under Path that are not among them — subdirectories,
	// symlinks and other non-regular entries are left alone, exactly like
	// the direct WithSourceGlob path (Rex prune_dir). Without Glob, Prune
	// has tree semantics and removes every entry with no counterpart in
	// the blob. Plans recorded before v24 carry no glob field and keep
	// tree semantics.
	Glob bool `json:"glob,omitempty"`
	// FileMode is an octal permission string applied to files copied by
	// KindSyncDir (same format as Mode, including the four-digit
	// special-bit form).
	FileMode string `json:"file_mode,omitempty"`
}

func (p SyncDirPayload) applyToWire(w *wireOp) {
	w.SourceDir = p.SourceDir
	w.Glob = p.Glob
	w.FileMode = p.FileMode
}

// FilePayload holds the wire fields exclusive to KindFile (task ae2, Layer
// 2's sixth and largest slice — file is gonf's most-used resource kind).
// resource/file's planwire.go is the only other non-test package that
// constructs one, always non-nil on a "file" op's Payload (record side:
// planHandler.ToOp; apply side: planHandler.Apply reads it through
// PayloadOf), so a decoded or freshly lowered KindFile op's Payload is never
// nil, keeping encode/decode round trips symmetric (see payloadFromWire).
// Packaging a recorded source file into an already-built op goes through
// SetFileContentB64 below, never a hand-written assert-and-write-back.
//
// KindEnsureFile does NOT get a FilePayload, despite resource/file's
// draft-side Payload (resource/file/payload.go, task w62 Layer 1) being
// filled unconditionally for both "file" and "ensure_file" drafts: on the
// WIRE side, ensureFileHandler.ToOp (resource/file/planwire.go) never reads
// the draft's Payload at all — an ensure_file op never carries content,
// template, validation, or line-edit intent, and ensureFileHandler.Apply
// never reads any of these fields either. The draft-side sharing (one Go
// type reused unmodified for two kinds) does not carry over to the wire
// side (one Kind, one payload) the way it might seem to at first glance;
// verified here by reading resource/file/planwire.go's actual handler
// registrations and field usage rather than assumed from the Layer 1
// precedent.
//
// Its json tags are never consulted by encoding/json — Op.MarshalJSON
// merges these fields onto a wireOp and marshals THAT (applyToWire below),
// never this struct directly — but api's secret-scan reflection walkers
// (scanOpStrings/redactOpStrings, opFieldClasses) still need them: it
// descends into Op.Payload's concrete value at the op's own top-level path
// (see api/secret_fields.go's scanStruct/redactStruct), and computes each
// leaf's classification path from THESE tags. They must therefore keep naming the
// same wire keys wireOp's own fields do; TestWirePayloadTagsMatch
// (types_test.go) pins that the two never drift apart.
//
// Several fields are multi-schema-version features (see CurrentVersion's
// history and the VersionKeyedLines constant, types.go): the version gate
// is purely about which wire BYTES an older destination refuses, not which
// Go type holds the field, so moving them onto a payload needed no version
// change (the same finding 6e2's ManageHome move already made).
//
// Field docs (unchanged from Op's pre-ae2 flat field comments):
type FilePayload struct {
	// ContentB64 is base64 file content for KindFile (InstallFile-style). A
	// legitimately empty file (WithContent("") or an empty WithSource file)
	// also base64-encodes to "", so this alone cannot tell "empty content"
	// apart from "no content recorded"; see HasContent.
	ContentB64 string `json:"content_b64,omitempty"`
	// HasContent marks that KindFile's content was explicitly configured
	// (WithContent or WithSource), even when it resolves to zero bytes and
	// ContentB64 is therefore "". Apply uses it to accept a legitimately
	// empty file while still erroring loudly when both ContentB64 and
	// Op.Blob are unset AND HasContent is false (a record-time bug).
	HasContent bool `json:"has_content,omitempty"`
	// Template marks that KindFile's content must be rendered as a
	// text/template on the destination (schema v9): the recipe's source or
	// destination path ended in ".tmpl" at record time. By apply time the
	// content already travels as raw template text in ContentB64/Op.Blob and
	// neither Op.Path nor an (empty, wire content is never re-sourced) source
	// path still carries the ".tmpl" suffix that would otherwise trigger
	// rendering, so this flag is what carries the intent across the wire.
	Template bool `json:"template,omitempty"`
	// TemplateParam is the recipe's declared source path, recorded alongside
	// Template so the destination render uses the same {{.Param}} default a
	// direct (non-plan) File with the same ".tmpl" source would use, instead
	// of exposing the plan-apply implementation detail (there is no source
	// file on the destination to derive it from).
	TemplateParam string `json:"template_param,omitempty"`
	// TemplateData is JSON-compatible data supplied by WithTemplateData
	// (schema v12).
	TemplateData json.RawMessage `json:"template_data,omitempty"`
	// ValidationBin and ValidationArgs are an optional file validator argv
	// (schema v18). ValidationArgs contains CandidatePath, which destination
	// apply replaces with a private staged filename before starting
	// ValidationBin.
	ValidationBin  string   `json:"validation_bin,omitempty"`
	ValidationArgs []string `json:"validation_args,omitempty"`
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
	// AddLine and RemoveLine are accepted when applying pre-v14 plans.
	// Current recording never sets them (resource.PlanDraft has no singular
	// fields); they stay on the wire type only so old recorded plans decode
	// and apply.
	AddLine    string `json:"add_line,omitempty"`
	RemoveLine string `json:"remove_line,omitempty"`
}

// SetFileContentB64 sets b64 onto op's FilePayload.ContentB64 in place. It
// is the one home for this knowledge (task eg2): api's packager and
// internal/testapply both package a recorded source file into an op AFTER
// its handler's ToOp built it, and each used to keep a verbatim copy of
// this body. ContentB64 lives on FilePayload since task ae2 and FilePayload
// is stored in op.Payload as a VALUE, so a caller cannot assign the field
// directly: it must assert, mutate a local copy and write that copy back.
// Dropping the write-back still compiles and silently loses the content,
// which is why two drifting copies were dangerous.
//
// An op without a FilePayload is left untouched. That covers an
// "ensure_file" op, whose ToOp never sets one (see FilePayload's doc
// comment): ensureFileHandler.Apply never reads ContentB64, so the field
// was already dead wire data on that kind before ae2 moved it.
func SetFileContentB64(op *Op, b64 string) {
	fp, ok := op.Payload.(FilePayload)
	if !ok {
		return
	}
	fp.ContentB64 = b64
	op.Payload = fp
}

func (p FilePayload) applyToWire(w *wireOp) {
	w.ContentB64 = p.ContentB64
	w.HasContent = p.HasContent
	w.Template = p.Template
	w.TemplateParam = p.TemplateParam
	w.TemplateData = p.TemplateData
	w.ValidationBin = p.ValidationBin
	w.ValidationArgs = p.ValidationArgs
	w.AddLines = p.AddLines
	w.RemoveLines = p.RemoveLines
	w.KeyedLines = p.KeyedLines
	w.AddLine = p.AddLine
	w.RemoveLine = p.RemoveLine
}

// payloadKindsByType is the reverse of the Kind -> OpPayload mapping
// OpPayloadExamples() builds (task cg2), keyed by each concrete OpPayload's
// own reflect.Type. It lets toWire's encode-side ownership check, below,
// name a mismatched payload's rightful kind the same way checkForeignPayload
// (task 2f2) already names a mismatched wire FIELD's rightful kind — one
// direction of the same OpPayloadExamples()-driven inventory, reflected the
// other way around. Built once, like payloadFieldOwners, so a follow-up task
// that extends OpPayloadExamples() to migrate one more kind is automatically
// covered on the encode side too, with nothing here to hand-maintain.
var payloadKindsByType = buildPayloadKindsByType()

func buildPayloadKindsByType() map[reflect.Type]Kind {
	kinds := make(map[reflect.Type]Kind)
	for kind, example := range OpPayloadExamples() {
		kinds[reflect.TypeOf(example)] = kind
	}
	return kinds
}

// toWire copies every Op core field onto a fresh wireOp and, when op.Payload
// is set, layers its kind-exclusive fields on top. It does not normalize;
// callers (MarshalJSON) do that once, after the merge.
//
// Before doing so it refuses an op.Payload whose concrete type is not the
// one op.Op's own kind owns (task cg2, the encode-side mirror of
// checkForeignPayload's decode-side refusal below): applyToWire only ever
// writes ITS OWN kind's wire fields (see each OpPayload implementation
// above), so a mismatched pairing — e.g. Op{Op: KindDir, Payload:
// FilePayload{...}} — used to reach here, apply FilePayload's fields onto a
// "dir" wireOp regardless of op.Op, and marshal a syntactically valid but
// semantically wrong line: one checkForeignPayload would then refuse on its
// own very next decode (see the task 2f2/cg2 annotations for the probe that
// found it — a "dir" line DecodeOp of its own EncodeOp output). Nothing on
// the record path rules that pairing out by construction (plan/sensitive.go
// RequiredVersion's own doc comments explain why it needs an identical Kind
// guard for the same reason), so encode is the last, and therefore the
// right, place to catch it — refusing here means an author of such a bug
// (a future payload-assigning migration, an op clone/merge helper, or a
// hand-built plan.Op in a consumer recipe) sees the error at the exact call
// that produced the bad Op, not three steps downstream at DecodeOp, and
// `gonf plan -o dir` can no longer succeed and write a plan.jsonl line that
// gonf's own apply, push or preview would then refuse to read back.
func (op Op) toWire() (wireOp, error) {
	if op.Payload != nil {
		gotType := reflect.TypeOf(op.Payload)
		owner, known := payloadKindsByType[gotType]
		if !known {
			// Unreachable in production: OpPayload (above) is a closed set —
			// only a type declared in this file can implement its unexported
			// applyToWire — and OpPayloadExamples() lists every one of them,
			// so payloadKindsByType always has an entry for any real
			// op.Payload value. Kept as a defensive refusal, never a panic
			// (AGENTS.md's registration-time contract), in case a future
			// OpPayload implementation is added here without a matching
			// OpPayloadExamples() entry.
			return wireOp{}, fmt.Errorf("%s op holds a payload of unrecognized type %s", op.Op, gotType)
		}
		if owner != op.Op {
			return wireOp{}, fmt.Errorf("%s op holds a foreign-kind payload: %s is exclusive to %s", op.Op, gotType.Name(), owner)
		}
	}
	w := wireOp{
		Op:      op.Op,
		Version: op.Version,
		ID:      op.ID,

		Path: op.Path,

		Mode:  op.Mode,
		Owner: op.Owner,
		Group: op.Group,

		Blob:   op.Blob,
		Prune:  op.Prune,
		Absent: op.Absent,

		Name: op.Name,
		Env:  op.Env,

		Command: op.Command,

		User:       op.User,
		Restart:    op.Restart,
		Reload:     op.Reload,
		EnableOnly: op.EnableOnly,
		IfChanged:  op.IfChanged,
		Watch:      op.Watch,

		Sensitive: op.Sensitive,
		Elevate:   op.Elevate,

		Deps: op.Deps,

		All:     op.All,
		Require: op.Require,
	}
	if op.Payload != nil {
		op.Payload.applyToWire(&w)
	}
	return w, nil
}

// fromWire is toWire's mirror: split a decoded, normalized wireOp into Op's
// core fields plus a concrete Payload built by payloadFromWire.
func fromWire(w wireOp) Op {
	return Op{
		Op:      w.Op,
		Version: w.Version,
		ID:      w.ID,

		Path: w.Path,

		Mode:  w.Mode,
		Owner: w.Owner,
		Group: w.Group,

		Blob:   w.Blob,
		Prune:  w.Prune,
		Absent: w.Absent,

		Name: w.Name,
		Env:  w.Env,

		Command: w.Command,

		User:       w.User,
		Restart:    w.Restart,
		Reload:     w.Reload,
		EnableOnly: w.EnableOnly,
		IfChanged:  w.IfChanged,
		Watch:      w.Watch,

		Sensitive: w.Sensitive,
		Elevate:   w.Elevate,

		Deps: w.Deps,

		All:     w.All,
		Require: w.Require,

		Payload: payloadFromWire(w),
	}
}

// payloadConstructors maps each Kind that has migrated exclusive fields off
// Op onto the constructor that rebuilds its concrete OpPayload from a
// decoded wireOp. payloadFromWire is driven from this table instead of a
// hand-written switch (task rf2: the switch it replaced had grown past
// CLAUDE.md's refactor-at-50-lines rule as more Layer 2 slices landed).
// Extending this table — one entry, mirroring CronPayload/
// SystemdTimerPayload/UserPayload's siblings — is the whole of what a
// follow-up task needs to migrate one more kind's exclusive fields, once
// wireOp itself already carries them (it always does: wireOp is unchanged
// by which kinds have migrated).
var payloadConstructors = map[Kind]func(wireOp) OpPayload{
	KindCron: func(w wireOp) OpPayload {
		return CronPayload{
			CronUser:      w.CronUser,
			LegacyCommand: w.LegacyCommand,
			Schedule:      w.Schedule,
			CronEnv:       w.CronEnv,
		}
	},
	KindSystemdTimer: func(w wireOp) OpPayload {
		return SystemdTimerPayload{
			OnCalendar:         w.OnCalendar,
			OnBootSec:          w.OnBootSec,
			Persistent:         w.Persistent,
			Description:        w.Description,
			ServiceDescription: w.ServiceDescription,
			After:              w.After,
			Wants:              w.Wants,
		}
	},
	KindUser: func(w wireOp) OpPayload {
		return UserPayload{
			PrimaryGroup:        w.PrimaryGroup,
			SupplementaryGroups: w.SupplementaryGroups,
			Home:                w.Home,
			CreateHome:          w.CreateHome,
			Shell:               w.Shell,
			LoginClass:          w.LoginClass,
			System:              w.System,
			ManageHome:          w.ManageHome,
		}
	},
	KindLink: func(w wireOp) OpPayload {
		return LinkPayload{Symlink: w.Symlink, Hardlink: w.Hardlink}
	},
	KindLinkIfExists: func(w wireOp) OpPayload {
		return LinkIfExistsPayload{Target: w.Target}
	},
	KindPackage: func(w wireOp) OpPayload {
		return PackagePayload{Latest: w.Latest}
	},
	KindCommand: func(w wireOp) OpPayload {
		return CommandPayload{
			Bin:     w.Bin,
			Args:    w.Args,
			Dir:     w.Dir,
			Creates: w.Creates,
			Unless:  w.Unless,
			OnlyIf:  w.OnlyIf,
		}
	},
	KindConfigSet: func(w wireOp) OpPayload {
		return ConfigSetPayload{
			Members:    w.Members,
			Validators: w.Validators,
			Chroot:     w.Chroot,
			StagingDir: w.StagingDir,
		}
	},
	KindConfigSetMember: func(w wireOp) OpPayload {
		return ConfigSetMemberPayload{Member: w.Member}
	},
	KindSyncDir: func(w wireOp) OpPayload {
		return SyncDirPayload{
			SourceDir: w.SourceDir,
			Glob:      w.Glob,
			FileMode:  w.FileMode,
		}
	},
	// KindEnsureFile deliberately gets no entry here — see FilePayload's own
	// doc comment for why the wire side does not reuse it the way
	// resource/file's draft-side Payload does.
	KindFile: func(w wireOp) OpPayload {
		return FilePayload{
			ContentB64:     w.ContentB64,
			HasContent:     w.HasContent,
			Template:       w.Template,
			TemplateParam:  w.TemplateParam,
			TemplateData:   w.TemplateData,
			ValidationBin:  w.ValidationBin,
			ValidationArgs: w.ValidationArgs,
			AddLines:       w.AddLines,
			RemoveLines:    w.RemoveLines,
			KeyedLines:     w.KeyedLines,
			AddLine:        w.AddLine,
			RemoveLine:     w.RemoveLine,
		}
	},
}

// payloadFromWire builds the concrete OpPayload for w.Op's Kind, or nil for
// a control kind or a kind that has not migrated any field off Op yet, by
// looking up payloadConstructors above.
//
// It keeps ONLY the fields belonging to w.Op's own entry, by construction —
// a wireOp decoded from a line that ALSO carries some other kind's
// exclusive field (e.g. a "cron" line with "on_calendar" set, which is
// SystemdTimerPayload's) has that field read here, discarded, and never
// reachable again. Before task 2f2 that was a silent encode/decode
// fidelity bug (see docs/design/plan.md, "Adding a resource kind (checklist)",
// task 9e2's note, and the task 2f2 annotation for the probe that found
// it): UnmarshalJSON (types.go) now calls checkForeignPayload, below,
// BEFORE reaching this function, and refuses a line carrying any such
// foreign-kind field instead of silently reaching this function to drop
// it — so a non-nil OpPayload built here is now guaranteed to be the only
// non-zero payload data w ever carried.
func payloadFromWire(w wireOp) OpPayload {
	if ctor, ok := payloadConstructors[w.Op]; ok {
		return ctor(w)
	}
	return nil
}

// payloadFieldOwner records which Kind's OpPayload a wireOp field (keyed by
// its Go field name, e.g. "OnCalendar") is exclusive to, the json tag
// checkForeignPayload's error should name it by, and the field's index in
// wireOp. It is built once, by reflecting OpPayloadExamples() (task 2f2's
// own single source of truth for "which kind owns which wire field,"
// already trusted by TestWirePayloadTagsMatch), so a follow-up task that
// extends OpPayloadExamples() to migrate one more kind is automatically
// covered here too — nothing in this file needs a second, hand-maintained
// list of exclusive fields that could drift from the first the way the
// yd2-era kind-dispatch switch above silently could.
//
// index is resolved once, at package init (task dg2), instead of looking the
// field up by name on every decode. The by-name lookup
// (reflect.Value.FieldByName over wireOp's ~70 fields, for each of ~50
// owners) made DecodeOp about 4x slower. Worse, FieldByName returns the
// invalid zero Value for a payload field with no wireOp counterpart, and
// IsZero PANICS on that Value, so such a drift crashed every decode of
// every OTHER kind instead of being reported.
type payloadFieldOwner struct {
	kind  Kind
	tag   string
	index int
}

// payloadFieldOwners and errPayloadFieldOwners are built together at init.
// A non-nil errPayloadFieldOwners is a programmer bug (a payload field
// added without its wireOp counterpart) that no plan line can cause.
// TestPayloadFieldOwnersResolve fails on it. checkForeignPayload then
// refuses every decode with it, rather than panicking or silently skipping
// the check: plan never panics for input (docs/design/plan.md, "Error handling
// contract"), and a package-init panic would kill every binary importing
// plan, including ones that never decode.
var payloadFieldOwners, errPayloadFieldOwners = buildPayloadFieldOwners(OpPayloadExamples())

// buildPayloadFieldOwners maps every field of every example payload to its
// owning kind, json tag and wireOp field index. It returns an error naming
// each payload field without a same-named, top-level (not promoted) wireOp
// field, so the drift surfaces once, with a clear message, instead of at
// decode time. The examples are a parameter so a test can feed in a
// deliberately drifted payload type.
func buildPayloadFieldOwners(examples map[Kind]OpPayload) (map[string]payloadFieldOwner, error) {
	wt := reflect.TypeFor[wireOp]()
	owners := make(map[string]payloadFieldOwner)
	var missing []string
	for kind, example := range examples {
		pt := reflect.TypeOf(example)
		for i := range pt.NumField() {
			f := pt.Field(i)
			wf, ok := wt.FieldByName(f.Name)
			if !ok || len(wf.Index) != 1 {
				missing = append(missing, pt.Name()+"."+f.Name)
				continue
			}
			tag, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			owners[f.Name] = payloadFieldOwner{kind: kind, tag: tag, index: wf.Index[0]}
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return owners, fmt.Errorf("plan: payload field(s) with no wireOp counterpart: %s", strings.Join(missing, ", "))
	}
	return owners, nil
}

// checkForeignPayload refuses a decoded wireOp that carries a non-zero
// value in a field exclusive to some OTHER kind's OpPayload than w.Op's
// own (see payloadFieldOwners above). payloadFromWire's kind-dispatch
// switch only ever extracts the fields belonging to w.Op's own kind, so
// any other kind's exclusive field surviving on the wire would otherwise
// be silently discarded on the next encode — a genuine decode/encode
// fidelity bug task yd2 introduced and task 2f2 fixed here (see the task
// 2f2 annotation for the probe that found it: a "cron" line carrying
// SystemdTimerPayload's on_calendar/persistent/description/after, and a
// "file" line carrying CronPayload's cron_user/on_calendar, both
// round-tripped at 540 bytes before yd2 and dropped to 327 bytes after).
//
// This mirrors the "an older destination would ignore the field ... so it
// must refuse" governing philosophy plan/types.go's own schema-version
// history already applies everywhere else in this package (see
// CurrentVersion's doc comment) — the same principle, just reached by a
// hand-edited or forged plan line carrying a foreign-kind field instead of
// an unsupported schema version. It is called from UnmarshalJSON
// (types.go) as a decode-time refusal, the same class of check DecodeOp
// already performs (missing op, empty line) — not a declerr report: no
// recipe has run yet, and this only rejects a wire line that
// toWire/applyToWire could never have produced from a legitimately built
// Op, since a payload's applyToWire (above) only ever writes ITS OWN
// kind's fields.
func checkForeignPayload(w wireOp) error {
	if errPayloadFieldOwners != nil {
		return fmt.Errorf("%s op: cannot check for foreign-kind fields: %w", w.Op, errPayloadFieldOwners)
	}
	wv := reflect.ValueOf(w)
	var bad []string
	for _, owner := range payloadFieldOwners {
		if owner.kind == w.Op {
			continue // w.Op's own exclusive field; payloadFromWire keeps it.
		}
		// owner.index was resolved against wireOp at init, so Field cannot
		// hit an out-of-range or invalid Value here (task dg2).
		if !wv.Field(owner.index).IsZero() {
			bad = append(bad, fmt.Sprintf("%s (%s-exclusive)", owner.tag, owner.kind))
		}
	}
	if len(bad) == 0 {
		return nil
	}
	sort.Strings(bad)
	return fmt.Errorf("%s op carries foreign-kind field(s) it cannot own: %s", w.Op, strings.Join(bad, ", "))
}

// OpPayloadExamples returns one zero-value OpPayload per Kind that has
// migrated exclusive fields off Op, keyed by Kind (a kind with no exclusive
// payload yet, or a control kind, has no entry). Production code never
// needs this; it exists so a reflection-based test that must reach every
// field of every concrete OpPayload type — api's TestOpFieldClassesAreExhaustive,
// which drives the secret-scan classification fitness check — can do so
// without api importing a resource/<kind> package or plan exposing anything
// beyond this small, deliberately test-shaped inventory (mirroring
// AllKinds's role for Kind itself).
func OpPayloadExamples() map[Kind]OpPayload {
	return map[Kind]OpPayload{
		KindCron:            CronPayload{},
		KindSystemdTimer:    SystemdTimerPayload{},
		KindUser:            UserPayload{},
		KindLink:            LinkPayload{},
		KindLinkIfExists:    LinkIfExistsPayload{},
		KindPackage:         PackagePayload{},
		KindCommand:         CommandPayload{},
		KindConfigSet:       ConfigSetPayload{},
		KindConfigSetMember: ConfigSetMemberPayload{},
		KindSyncDir:         SyncDirPayload{},
		KindFile:            FilePayload{},
	}
}
