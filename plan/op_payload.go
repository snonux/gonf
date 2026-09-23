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

// toWire copies every Op core field onto a fresh wireOp and, when op.Payload
// is set, layers its kind-exclusive fields on top. It does not normalize;
// callers (MarshalJSON) do that once, after the merge.
func (op Op) toWire() wireOp {
	w := wireOp{
		Op:      op.Op,
		Version: op.Version,
		ID:      op.ID,

		Path:     op.Path,
		Symlink:  op.Symlink,
		Target:   op.Target,
		Hardlink: op.Hardlink,

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
		Latest:         op.Latest,

		PrimaryGroup:        op.PrimaryGroup,
		SupplementaryGroups: op.SupplementaryGroups,
		Home:                op.Home,
		CreateHome:          op.CreateHome,
		Shell:               op.Shell,
		LoginClass:          op.LoginClass,
		System:              op.System,
		ManageHome:          op.ManageHome,

		AddLines:    op.AddLines,
		RemoveLines: op.RemoveLines,
		KeyedLines:  op.KeyedLines,
		AddLine:     op.AddLine,
		RemoveLine:  op.RemoveLine,

		Name: op.Name,
		Bin:  op.Bin,
		Args: op.Args,
		Dir:  op.Dir,
		Env:  op.Env,

		Creates: op.Creates,
		Unless:  op.Unless,
		OnlyIf:  op.OnlyIf,

		Command: op.Command,

		User:       op.User,
		Restart:    op.Restart,
		Reload:     op.Reload,
		EnableOnly: op.EnableOnly,
		IfChanged:  op.IfChanged,
		Watch:      op.Watch,

		Sensitive: op.Sensitive,
		Elevate:   op.Elevate,

		Members:    op.Members,
		Validators: op.Validators,
		Chroot:     op.Chroot,
		StagingDir: op.StagingDir,
		Member:     op.Member,

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

		Path:     w.Path,
		Symlink:  w.Symlink,
		Target:   w.Target,
		Hardlink: w.Hardlink,

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
		Latest:         w.Latest,

		PrimaryGroup:        w.PrimaryGroup,
		SupplementaryGroups: w.SupplementaryGroups,
		Home:                w.Home,
		CreateHome:          w.CreateHome,
		Shell:               w.Shell,
		LoginClass:          w.LoginClass,
		System:              w.System,
		ManageHome:          w.ManageHome,

		AddLines:    w.AddLines,
		RemoveLines: w.RemoveLines,
		KeyedLines:  w.KeyedLines,
		AddLine:     w.AddLine,
		RemoveLine:  w.RemoveLine,

		Name: w.Name,
		Bin:  w.Bin,
		Args: w.Args,
		Dir:  w.Dir,
		Env:  w.Env,

		Creates: w.Creates,
		Unless:  w.Unless,
		OnlyIf:  w.OnlyIf,

		Command: w.Command,

		User:       w.User,
		Restart:    w.Restart,
		Reload:     w.Reload,
		EnableOnly: w.EnableOnly,
		IfChanged:  w.IfChanged,
		Watch:      w.Watch,

		Sensitive: w.Sensitive,
		Elevate:   w.Elevate,

		Members:    w.Members,
		Validators: w.Validators,
		Chroot:     w.Chroot,
		StagingDir: w.StagingDir,
		Member:     w.Member,

		Deps: w.Deps,

		All:     w.All,
		Require: w.Require,

		Payload: payloadFromWire(w),
	}
}

// payloadFromWire builds the concrete OpPayload for w.Op's Kind, or nil for
// a control kind or a kind that has not migrated any field off Op yet.
// Extending this switch (and CronPayload/SystemdTimerPayload's siblings) is
// the whole of what a follow-up task needs to migrate one more kind's
// exclusive fields, once wireOp itself already carries them (it always
// does: wireOp is unchanged by which kinds have migrated).
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
		KindCron:         CronPayload{},
		KindSystemdTimer: SystemdTimerPayload{},
	}
}
