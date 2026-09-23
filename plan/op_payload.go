package plan

// OpPayload holds the wire fields exclusive to one Op's Kind (task yd2,
// "Layer 2" of the PlanDraft/Op god-struct split — see docs/plan.md, "The
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

// CronPayload holds the wire fields exclusive to KindCron. resource/cron's
// planwire.go is the only other package that constructs or reads one — it
// always sets a non-nil CronPayload on a "cron" op's Payload (record side:
// ToOp; apply side: Apply type-asserts it), so a decoded or freshly lowered
// KindCron op's Payload is never nil, keeping encode/decode round trips
// symmetric (see payloadFromWire).
//
// Its json tags are never consulted by encoding/json — Op.MarshalJSON
// merges these fields onto a wireOp and marshals THAT (applyToWire below),
// never this struct directly — but api's secret-scan reflection walker
// (walkOpStrings/opFieldClasses) still needs them: it descends into
// Op.Payload's concrete value at the op's own top-level path (see
// api/secret_fields.go's walkStruct), and computes each leaf's
// classification path from THESE tags. They must therefore keep naming the
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
// never this struct directly — but api's secret-scan reflection walker
// (walkOpStrings/opFieldClasses) still needs them: it descends into
// Op.Payload's concrete value at the op's own top-level path (see
// api/secret_fields.go's walkStruct), and computes each leaf's
// classification path from THESE tags. They must therefore keep naming the
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
// but api's secret-scan reflection walker (walkOpStrings/opFieldClasses)
// still needs them: it descends into Op.Payload's concrete value at the
// op's own top-level path (see api/secret_fields.go's walkStruct), and
// computes each leaf's classification path from THESE tags. They must
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

// toWire copies every Op core field onto a fresh wireOp and, when op.Payload
// is set, layers its kind-exclusive fields on top. It does not normalize;
// callers (MarshalJSON) do that once, after the merge.
func (op Op) toWire() wireOp {
	w := wireOp{
		Op:      op.Op,
		Version: op.Version,
		ID:      op.ID,

		Path: op.Path,

		Mode:     op.Mode,
		FileMode: op.FileMode,
		Owner:    op.Owner,
		Group:    op.Group,

		ContentB64:     op.ContentB64,
		Blob:           op.Blob,
		HasContent:     op.HasContent,
		Template:       op.Template,
		TemplateParam:  op.TemplateParam,
		TemplateData:   op.TemplateData,
		ValidationBin:  op.ValidationBin,
		ValidationArgs: op.ValidationArgs,
		SourceDir:      op.SourceDir,
		Glob:           op.Glob,
		Prune:          op.Prune,
		Absent:         op.Absent,

		AddLines:    op.AddLines,
		RemoveLines: op.RemoveLines,
		KeyedLines:  op.KeyedLines,
		AddLine:     op.AddLine,
		RemoveLine:  op.RemoveLine,

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
	return w
}

// fromWire is toWire's mirror: split a decoded, normalized wireOp into Op's
// core fields plus a concrete Payload built by payloadFromWire.
func fromWire(w wireOp) Op {
	return Op{
		Op:      w.Op,
		Version: w.Version,
		ID:      w.ID,

		Path: w.Path,

		Mode:     w.Mode,
		FileMode: w.FileMode,
		Owner:    w.Owner,
		Group:    w.Group,

		ContentB64:     w.ContentB64,
		Blob:           w.Blob,
		HasContent:     w.HasContent,
		Template:       w.Template,
		TemplateParam:  w.TemplateParam,
		TemplateData:   w.TemplateData,
		ValidationBin:  w.ValidationBin,
		ValidationArgs: w.ValidationArgs,
		SourceDir:      w.SourceDir,
		Glob:           w.Glob,
		Prune:          w.Prune,
		Absent:         w.Absent,

		AddLines:    w.AddLines,
		RemoveLines: w.RemoveLines,
		KeyedLines:  w.KeyedLines,
		AddLine:     w.AddLine,
		RemoveLine:  w.RemoveLine,

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

// payloadFromWire builds the concrete OpPayload for w.Op's Kind, or nil for
// a control kind or a kind that has not migrated any field off Op yet.
// Extending this switch (and CronPayload/SystemdTimerPayload/UserPayload's
// siblings) is the whole of what a follow-up task needs to migrate one more
// kind's exclusive fields, once wireOp itself already carries them (it
// always does: wireOp is unchanged by which kinds have migrated).
func payloadFromWire(w wireOp) OpPayload {
	switch w.Op {
	case KindCron:
		return CronPayload{
			CronUser:      w.CronUser,
			LegacyCommand: w.LegacyCommand,
			Schedule:      w.Schedule,
			CronEnv:       w.CronEnv,
		}
	case KindSystemdTimer:
		return SystemdTimerPayload{
			OnCalendar:         w.OnCalendar,
			OnBootSec:          w.OnBootSec,
			Persistent:         w.Persistent,
			Description:        w.Description,
			ServiceDescription: w.ServiceDescription,
			After:              w.After,
			Wants:              w.Wants,
		}
	case KindUser:
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
	case KindLink:
		return LinkPayload{
			Symlink:  w.Symlink,
			Hardlink: w.Hardlink,
		}
	case KindLinkIfExists:
		return LinkIfExistsPayload{
			Target: w.Target,
		}
	case KindPackage:
		return PackagePayload{
			Latest: w.Latest,
		}
	case KindCommand:
		return CommandPayload{
			Bin:     w.Bin,
			Args:    w.Args,
			Dir:     w.Dir,
			Creates: w.Creates,
			Unless:  w.Unless,
			OnlyIf:  w.OnlyIf,
		}
	case KindConfigSet:
		return ConfigSetPayload{
			Members:    w.Members,
			Validators: w.Validators,
			Chroot:     w.Chroot,
			StagingDir: w.StagingDir,
		}
	case KindConfigSetMember:
		return ConfigSetMemberPayload{
			Member: w.Member,
		}
	default:
		return nil
	}
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
	}
}
