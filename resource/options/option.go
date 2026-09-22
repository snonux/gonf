// Package options provides typed configuration options for gonf resources.
//
// Each resource family accepts its own option interface. Shared options
// implement every family they support, so invalid option/resource pairs are
// rejected by the compiler instead of failing during recipe registration.
package options

import (
	"fmt"
	"os"
	"slices"

	"github.com/snonux/gonf/internal/logger"
	"github.com/snonux/gonf/resource"
)

// CandidatePath is the sole placeholder allowed in WithValidation arguments.
// Gonf replaces it with a freshly staged, private candidate path at apply
// time. It is deliberately not a usable filesystem path in a recipe.
const CandidatePath = "\x00gonf-candidate-path\x00"

// Argument-less options are package-level values rather than functions: each
// one sets a single flag on the resource it is applied to.

// WithCreateHome creates the configured (or platform-default) home directory
// only while creating a missing user.
var WithCreateHome = userAccountOption(func(target any) {
	requires(target, "WithCreateHome", func(r CreateHomeable) { r.SetCreateHome() })
})

// WithSystem requests a system account while creating a missing user.
// BSD backends reject it because their supported account utilities do not
// expose one portable system-account mode.
var WithSystem = userAccountOption(func(target any) {
	requires(target, "WithSystem", func(r Systemable) { r.SetSystem() })
})

// WithTemplate forces file content to render as a text/template.
var WithTemplate = fileOption(func(target any) {
	requires(target, "WithTemplate", func(r Templateable) { r.SetTemplate() })
})

// WithPrune enables source reconciliation for a directory.
var WithPrune = dirOption(func(target any) {
	requires(target, "WithPrune", func(r Prunable) { r.SetPrune() })
})

// IsAbsent marks a resource for removal.
var IsAbsent = absentOption(func(target any) {
	requires(target, "IsAbsent", func(r Absentable) { r.SetAbsent() })
})

// IsLatest marks a package for upgrade to the newest version.
var IsLatest = packageOption(func(target any) {
	requires(target, "IsLatest", func(r Latestable) { r.SetLatest() })
})

// WithRestart restarts a service or timer after convergence.
var WithRestart = serviceTimerOption(func(target any) {
	requires(target, "WithRestart", func(r Restartable) { r.SetRestart() })
})

// WithReload reloads a service after convergence.
var WithReload = serviceOption(func(target any) {
	requires(target, "WithReload", func(r Reloadable) { r.SetReload() })
})

// WithUser selects the systemd user bus.
var WithUser = userOption(func(target any) {
	requires(target, "WithUser", func(r UserService) { r.SetUser() })
})

// WithElevate marks a command for privileged execution.
var WithElevate = commandOption(func(target any) {
	requires(target, "WithElevate", func(r Elevatable) { r.SetElevate() })
})

// WithEnableOnly makes a timer converge enable/disable without start/stop.
var WithEnableOnly = enableOnlyOption(func(target any) {
	requires(target, "WithEnableOnly", func(r EnableOnlyable) { r.SetEnableOnly() })
})

// IfChanged is the legacy daemon-reload spelling of the change gate: it
// arms the gate without naming watched ids, so the reload watches its
// WithWatch ids or, failing those, its DependsOn ids. It lowers to the same
// SetChangeWatch call as WatchChanges (IfChanged plus WithWatch(ids...) is
// exactly WatchChanges(ids...), and IfChanged plus DependsOn(r) records the
// same op as OnChange(r)). New recipes should use OnChange. Like WithWatch
// it requires ChangeGated, so a Service, Timer or Command reached through
// the type-erased Option path still refuses it ("does not support
// IfChanged"), whatever other options come with it.
var IfChanged = daemonReloadOption(legacyChangeGate("IfChanged", nil))

// WithPersistent enables Persistent=true on a systemd timer.
var WithPersistent = systemdTimerOption(func(target any) {
	requires(target, "WithPersistent", func(r Persistentable) { r.SetPersistent() })
})

// Option is the callable, erased legacy option operation retained for older
// recipes. Prefer the resource-family interfaces (FileOption, DirOption, and
// so on) everywhere a resource is constructed: values stored as Option have
// lost the compile-time family check and are an unsafe compatibility escape
// hatch. Pass legacy values through the matching To*Options adapter.
type Option = func(any)

// Capability interfaces describe the setters used by individual options.
// They remain small so resource types only implement the capabilities they
// actually support.
type (
	Owner            interface{ SetOwner(string) }
	Grouped          interface{ SetGroup(string) }
	Moded            interface{ SetMode(os.FileMode) }
	Sourced          interface{ SetSource(string) }
	SourceGlobable   interface{ SetSourceGlob(string) }
	SourceBaseable   interface{ SetSourceBase(string) }
	Paramable        interface{ SetParam(string) }
	Templateable     interface{ SetTemplate() }
	TemplateDataable interface{ SetTemplateData(any) }
	Validatable      interface{ SetValidation(string, []string) }
	Contented        interface{ SetContent(string) }
	LineAddable      interface{ SetAddLine(string) }
	LineRemovable    interface{ SetRemoveLine(string) }
	LinesAddable     interface{ AddLines(...string) }
	LinesRemovable   interface{ RemoveLines(...string) }
	FileModed        interface{ SetFileMode(os.FileMode) }
	Prunable         interface{ SetPrune() }
	Absentable       interface{ SetAbsent() }
	Latestable       interface{ SetLatest() }
	Dependable       interface{ AddDependency(string) }
	Named            interface{ SetName(string) }
	Dirable          interface{ SetDir(string) }
	Envable          interface{ SetEnv(map[string]string) }
	Creatable        interface{ SetCreates(string) }
	Guardable        interface {
		SetUnless(*Guard)
		SetOnlyIf(*Guard)
	}
	Linkable interface {
		SetSymlink(string)
		SetHardlink(string)
	}
	Restartable    interface{ SetRestart() }
	Reloadable     interface{ SetReload() }
	UserService    interface{ SetUser() }
	EnableOnlyable interface{ SetEnableOnly() }
	// ChangeWatchable is the one capability every change-gate option
	// (OnChange, WatchChanges, IfChanged, WithWatch) lowers to: arm the gate
	// and add the watched ids (embed.ChangeGate implements it).
	ChangeWatchable interface{ SetChangeWatch([]string) }
	// DependsOnWatcher marks a change-gated resource that, armed without
	// watched ids, watches its DependsOn ids instead (daemon-reload only).
	// It is a marker, not a setter: it admits the legacy spellings.
	DependsOnWatcher interface{ WatchesDependsOn() }
	// ChangeGated is what the legacy daemon-reload spellings IfChanged and
	// WithWatch require: the one change-gate capability plus the DependsOn
	// fallback. Their meaning (arm without ids) relies on that fallback, so
	// they stay daemon-reload-only at run time too, as they were when
	// ChangeGated was the SetIfChanged setter.
	ChangeGated interface {
		ChangeWatchable
		DependsOnWatcher
	}
	// Watchable is retained as an alias of ChangeGated: WithWatch, which
	// once required a SetWatch setter, now requires ChangeGated.
	Watchable              = ChangeGated
	Elevatable             interface{ SetElevate() }
	CronUserable           interface{ SetCronUser(string) }
	LegacyCronCommandable  interface{ SetLegacyCommand(string) }
	Commandable            interface{ SetCommand(string) }
	Minuteable             interface{ SetMinute(string) }
	Hourable               interface{ SetHour(string) }
	Monthdayable           interface{ SetMonthday(string) }
	Monthable              interface{ SetMonth(string) }
	Weekdayable            interface{ SetWeekday(string) }
	CronEnvable            interface{ AddCronEnv(string) }
	Homeable               interface{ SetHome(string) }
	CreateHomeable         interface{ SetCreateHome() }
	Shellable              interface{ SetShell(string) }
	Classable              interface{ SetLoginClass(string) }
	Systemable             interface{ SetSystem() }
	SupplementaryGroupable interface{ AddSupplementaryGroups(...string) }

	OnCalendarable         interface{ SetOnCalendar(string) }
	OnBootSecable          interface{ SetOnBootSec(string) }
	Persistentable         interface{ SetPersistent() }
	Descriptionable        interface{ SetDescription(string) }
	ServiceDescriptionable interface{ SetServiceDescription(string) }
	Afterable              interface{ AddAfter(...string) }
	Wantsable              interface{ AddWants(...string) }
)

// Resource-family option interfaces. The unexported marker methods prevent
// callers from accidentally manufacturing an option for an unrelated family.
type (
	FileOption interface {
		Apply(any)
		fileOption()
	}
	DirOption interface {
		Apply(any)
		dirOption()
	}
	LinkOption interface {
		Apply(any)
		linkOption()
	}
	PackageOption interface {
		Apply(any)
		packageOption()
	}
	ServiceOption interface {
		Apply(any)
		serviceOption()
	}
	CronOption interface {
		Apply(any)
		cronOption()
	}
	TimerOption interface {
		Apply(any)
		timerOption()
	}
	SystemdTimerOption interface {
		Apply(any)
		systemdTimerOption()
	}
	DaemonReloadOption interface {
		Apply(any)
		daemonReloadOption()
	}
	CommandOption interface {
		Apply(any)
		commandOption()
	}
	// LocalUserOption configures an additive-only local user resource.
	LocalUserOption interface {
		Apply(any)
		localUserOption()
	}

	AllResourceOption interface {
		FileOption
		DirOption
		LinkOption
		PackageOption
		ServiceOption
		CronOption
		TimerOption
		SystemdTimerOption
		DaemonReloadOption
		CommandOption
		LocalUserOption
	}
	FileDirOption interface {
		FileOption
		DirOption
	}
	AbsentOption interface {
		FileOption
		DirOption
		LinkOption
		PackageOption
		ServiceOption
		CronOption
		TimerOption
		SystemdTimerOption
	}
	ServiceTimerOption interface {
		ServiceOption
		TimerOption
		SystemdTimerOption
	}
	UserOption interface {
		ServiceOption
		TimerOption
		SystemdTimerOption
		DaemonReloadOption
	}
	ChangeGateOption interface {
		CommandOption
		ServiceOption
		TimerOption
		DaemonReloadOption
	}
	EnableOnlyOption interface {
		TimerOption
		SystemdTimerOption
	}
	CronSystemdTimerOption interface {
		CronOption
		SystemdTimerOption
	}
)

// Concrete option types. Each is a func(any) whose unexported marker methods
// (below) decide which resource families it satisfies, so one value can be
// accepted by several families without an unrelated family accepting it.
type (
	allResourceOption      func(any)
	fileDirOption          func(any)
	fileOption             func(any)
	fileCommandOption      func(any)
	dirOption              func(any)
	linkOption             func(any)
	packageOption          func(any)
	packageCommandOption   func(any)
	serviceOption          func(any)
	cronOption             func(any)
	systemdTimerOption     func(any)
	daemonReloadOption     func(any)
	commandOption          func(any)
	groupOption            func(any)
	userAccountOption      func(any)
	absentOption           func(any)
	serviceTimerOption     func(any)
	userOption             func(any)
	enableOnlyOption       func(any)
	cronSystemdTimerOption func(any)
	changeGateOption       func(any)
)

// Guard describes an Unless/OnlyIf probe.
type Guard struct {
	Name         string
	Args         []string
	ExpectExit   int
	ExpectStdout string
}

// GuardOption configures a Guard built by Unless or OnlyIf.
type GuardOption func(*Guard)

// newGuard builds the Guard for Unless/OnlyIf: it probes name with args and,
// before opts are applied, expects exit code 0 and places no constraint on
// stdout.
func newGuard(name string, args []string, opts ...GuardOption) *Guard {
	g := &Guard{Name: name, Args: args, ExpectExit: 0}
	for _, opt := range opts {
		opt(g)
	}
	return g
}

// Apply runs the option against target. Every concrete option type forwards to
// its underlying func; target must implement the capability interface the
// option needs, or requires aborts the recipe.
func (o allResourceOption) Apply(target any)      { o(target) }
func (o fileDirOption) Apply(target any)          { o(target) }
func (o fileOption) Apply(target any)             { o(target) }
func (o fileCommandOption) Apply(target any)      { o(target) }
func (o dirOption) Apply(target any)              { o(target) }
func (o linkOption) Apply(target any)             { o(target) }
func (o packageOption) Apply(target any)          { o(target) }
func (o packageCommandOption) Apply(target any)   { o(target) }
func (o serviceOption) Apply(target any)          { o(target) }
func (o cronOption) Apply(target any)             { o(target) }
func (o systemdTimerOption) Apply(target any)     { o(target) }
func (o daemonReloadOption) Apply(target any)     { o(target) }
func (o commandOption) Apply(target any)          { o(target) }
func (o groupOption) Apply(target any)            { o(target) }
func (o userAccountOption) Apply(target any)      { o(target) }
func (o absentOption) Apply(target any)           { o(target) }
func (o serviceTimerOption) Apply(target any)     { o(target) }
func (o userOption) Apply(target any)             { o(target) }
func (o enableOnlyOption) Apply(target any)       { o(target) }
func (o cronSystemdTimerOption) Apply(target any) { o(target) }
func (o changeGateOption) Apply(target any)       { o(target) }

// ToFileOptions adapts erased options kept in legacy []Option slices to the
// typed file-option slice accepted by file resources. Prefer passing typed
// options directly: an erased option no longer carries enough information for
// this adapter to validate its resource family.
func ToFileOptions(opts ...Option) []FileOption {
	return toLegacyOptions(opts, func(fn func(any)) FileOption { return fileOption(fn) })
}

// ToDirOptions adapts erased options to directory options.
func ToDirOptions(opts ...Option) []DirOption {
	return toLegacyOptions(opts, func(fn func(any)) DirOption { return dirOption(fn) })
}

// ToLinkOptions adapts erased options to link options.
func ToLinkOptions(opts ...Option) []LinkOption {
	return toLegacyOptions(opts, func(fn func(any)) LinkOption { return linkOption(fn) })
}

// ToPackageOptions adapts erased options to package options.
func ToPackageOptions(opts ...Option) []PackageOption {
	return toLegacyOptions(opts, func(fn func(any)) PackageOption { return packageOption(fn) })
}

// ToServiceOptions adapts erased options to service options.
func ToServiceOptions(opts ...Option) []ServiceOption {
	return toLegacyOptions(opts, func(fn func(any)) ServiceOption { return serviceOption(fn) })
}

// ToCronOptions adapts erased options to cron options.
func ToCronOptions(opts ...Option) []CronOption {
	return toLegacyOptions(opts, func(fn func(any)) CronOption { return cronOption(fn) })
}

// ToTimerOptions adapts erased options to timer options.
func ToTimerOptions(opts ...Option) []TimerOption {
	return toLegacyOptions(opts, func(fn func(any)) TimerOption { return serviceTimerOption(fn) })
}

// ToSystemdTimerOptions adapts erased options to systemd-timer options.
func ToSystemdTimerOptions(opts ...Option) []SystemdTimerOption {
	return toLegacyOptions(opts, func(fn func(any)) SystemdTimerOption { return systemdTimerOption(fn) })
}

// ToDaemonReloadOptions adapts erased options to daemon-reload options.
func ToDaemonReloadOptions(opts ...Option) []DaemonReloadOption {
	return toLegacyOptions(opts, func(fn func(any)) DaemonReloadOption { return daemonReloadOption(fn) })
}

// ToCommandOptions adapts erased options to command options.
func ToCommandOptions(opts ...Option) []CommandOption {
	return toLegacyOptions(opts, func(fn func(any)) CommandOption { return commandOption(fn) })
}

// ToLocalUserOptions adapts erased options to local-user options.
func ToLocalUserOptions(opts ...Option) []LocalUserOption {
	return toLegacyOptions(opts, func(fn func(any)) LocalUserOption { return userAccountOption(fn) })
}

// ExpectExit sets the exit code that makes a guard succeed (default 0).
func ExpectExit(code int) GuardOption {
	return func(g *Guard) { g.ExpectExit = code }
}

// ExpectStdout requires trimmed stdout to equal want for the guard to succeed.
func ExpectStdout(want string) GuardOption {
	return func(g *Guard) { g.ExpectStdout = want }
}

// DependsOn declares that the resource depends on every supplied resource.
func DependsOn(deps ...resource.Dependency) allResourceOption {
	return allResourceOption(func(target any) {
		requires(target, "DependsOn", func(r Dependable) {
			for _, dep := range deps {
				for _, id := range dep.Dependencies() {
					r.AddDependency(id)
				}
			}
		})
	})
}

// WithOwner sets the owning user of a file or directory resource.
func WithOwner(owner string) fileDirOption {
	return fileDirOption(func(target any) {
		requires(target, "WithOwner", func(r Owner) { r.SetOwner(owner) })
	})
}

// WithGroup sets the owning group of a file or directory resource, or the
// creation-time primary group of a user resource.
func WithGroup(group string) groupOption {
	return groupOption(func(target any) {
		requires(target, "WithGroup", func(r Grouped) { r.SetGroup(group) })
	})
}

// WithHome sets a user's home directory when creating a missing account.
// It does not create the directory; use WithCreateHome to request that. An
// existing account's home field is left alone unless WithManageHome is also
// given (see resource/options/user.go).
func WithHome(home string) userAccountOption {
	return userAccountOption(func(target any) {
		requires(target, "WithHome", func(r Homeable) { r.SetHome(home) })
	})
}

// WithShell sets a user's login shell when creating a missing account.
func WithShell(shell string) userAccountOption {
	return userAccountOption(func(target any) {
		requires(target, "WithShell", func(r Shellable) { r.SetShell(shell) })
	})
}

// WithLoginClass sets a user's platform login class when creating a missing
// account. Linux rejects this option because it has no login classes.
func WithLoginClass(class string) userAccountOption {
	return userAccountOption(func(target any) {
		requires(target, "WithLoginClass", func(r Classable) { r.SetLoginClass(class) })
	})
}

// WithClass is a compatibility alias for WithLoginClass.
func WithClass(class string) userAccountOption { return WithLoginClass(class) }

// WithPrimaryGroup sets a user's creation-time primary group.
func WithPrimaryGroup(group string) userAccountOption {
	return userAccountOption(func(target any) {
		requires(target, "WithPrimaryGroup", func(r Grouped) { r.SetGroup(group) })
	})
}

// WithSupplementaryGroups adds memberships for a user. Existing memberships
// not named here are retained.
func WithSupplementaryGroups(groups ...string) userAccountOption {
	return userAccountOption(func(target any) {
		requires(target, "WithSupplementaryGroups", func(r SupplementaryGroupable) {
			r.AddSupplementaryGroups(groups...)
		})
	})
}

// WithUserGroup adds one supplementary group membership for a user. Existing
// memberships are retained; use WithPrimaryGroup for the creation-time
// primary group.
func WithUserGroup(group string) userAccountOption {
	return WithSupplementaryGroups(group)
}

// WithMode sets a resource's own file mode.
func WithMode(mode os.FileMode) fileDirOption {
	return fileDirOption(func(target any) {
		requires(target, "WithMode", func(r Moded) { r.SetMode(NormalizeMode(mode)) })
	})
}

// WithSource sets the source path for a file or directory resource.
func WithSource(source string) fileDirOption {
	return fileDirOption(func(target any) {
		requires(target, "WithSource", func(r Sourced) { r.SetSource(source) })
	})
}

// WithSourceGlob copies matching files into a directory.
func WithSourceGlob(pattern string) dirOption {
	return dirOption(func(target any) {
		requires(target, "WithSourceGlob", func(r SourceGlobable) { r.SetSourceGlob(pattern) })
	})
}

// WithParam overrides the template parameter used by a file resource.
func WithParam(value string) fileOption {
	return fileOption(func(target any) {
		requires(target, "WithParam", func(r Paramable) { r.SetParam(value) })
	})
}

// WithTemplateData supplies JSON-compatible data to a file template. It also
// enables template rendering, so literal template content need not carry a
// .tmpl suffix. The value is validated while recording the plan and rendered
// only by the destination.
func WithTemplateData(data any) fileOption {
	return fileOption(func(target any) {
		requires(target, "WithTemplateData", func(r TemplateDataable) { r.SetTemplateData(data) })
	})
}

// WithValidation validates a rendered File candidate with bin and args before
// publishing it at the resource's live path. args must contain CandidatePath
// exactly once; the validator is invoked directly with argv, never through a
// shell. Validation is supported only for content-managed absolute files, not
// line edits, absence, or preserve-content resources.
func WithValidation(bin string, args []string) fileOption {
	return fileOption(func(target any) {
		requires(target, "WithValidation", func(r Validatable) { r.SetValidation(bin, args) })
	})
}

// WithSourceBase sets the declared source directory for a synced directory.
func WithSourceBase(value string) dirOption {
	return dirOption(func(target any) {
		requires(target, "WithSourceBase", func(r SourceBaseable) { r.SetSourceBase(value) })
	})
}

// WithContent sets literal file content.
func WithContent(content string) fileOption {
	return fileOption(func(target any) {
		requires(target, "WithContent", func(r Contented) { r.SetContent(content) })
	})
}

// WithLines appends each line of file content when it is missing.
func WithLines(lines ...string) fileOption {
	return fileOption(func(target any) {
		requires(target, "WithLines", func(r LinesAddable) { r.AddLines(lines...) })
	})
}

// WithoutLines removes each matching line from file content.
func WithoutLines(lines ...string) fileOption {
	return fileOption(func(target any) {
		requires(target, "WithoutLines", func(r LinesRemovable) { r.RemoveLines(lines...) })
	})
}

// WithLine appends a line of file content. It is retained for compatibility.
func WithLine(content string) fileOption {
	return fileOption(func(target any) {
		requires(target, "WithLine", func(r LineAddable) { r.SetAddLine(content) })
	})
}

// WithoutLine removes a line from file content. It is retained for compatibility.
func WithoutLine(content string) fileOption {
	return fileOption(func(target any) {
		requires(target, "WithoutLine", func(r LineRemovable) { r.SetRemoveLine(content) })
	})
}

// WithFileMode sets the mode of regular files copied into a directory.
func WithFileMode(mode os.FileMode) dirOption {
	return dirOption(func(target any) {
		requires(target, "WithFileMode", func(r FileModed) { r.SetFileMode(NormalizeMode(mode)) })
	})
}

// WithWatch is the legacy daemon-reload spelling of WatchChanges: with ids
// it arms the gate and adds them to the watch list, exactly like
// WatchChanges(ids...) (so IfChanged next to it is redundant but harmless),
// and calls accumulate, as every change-gate option does. It differs from
// WatchChanges only without ids: WithWatch() stays what it always was, a
// no-op (the reload's gate is left as the other options set it), where
// WatchChanges() aborts. Like IfChanged it requires ChangeGated, so it is
// refused on every resource but daemon-reload. New recipes should use
// OnChange.
func WithWatch(ids ...string) daemonReloadOption {
	if len(ids) == 0 {
		return daemonReloadOption(func(target any) {
			requires(target, "WithWatch", func(ChangeGated) {})
		})
	}
	return daemonReloadOption(legacyChangeGate("WithWatch", ids))
}

// OnChange gates a resource's mutating action on the change reports of the
// supplied resources: the action fires only when one of them changed (or
// would change, under dry-run) during this apply. It also records the
// watched resources as regular dependencies, so they apply first (via
// DependsOn ordering, on the plan wire's deps field). It is WatchChanges
// on the resources' ids plus DependsOn(resources...).
//
// Per resource family the gated action is:
//
//   - Command: the command is skipped entirely unless a watched resource
//     changed.
//   - Service / Timer: state still converges (started/enabled/stopped), but
//     WithRestart / WithReload fire only on a watched change.
//   - DaemonReload: the reload is skipped unless a watched resource changed.
//
// OnChange requires at least one resource: a gate with nothing to watch can
// never fire, so it is registration-time misuse (fail-fast DSL contract).
func OnChange(resources ...resource.Dependency) changeGateOption {
	return changeGateOption(func(target any) {
		var ids []string
		for _, res := range resources {
			ids = append(ids, res.Dependencies()...)
		}
		if len(ids) == 0 {
			logger.Fatal("OnChange requires at least one resource to watch")
		}
		changeGate("OnChange", ids)(target)
		// The watched resources must also apply first: reuse the ordinary
		// dependency accumulation so ordering (and the plan wire's deps
		// field) flows through the existing DependsOn embed.
		requires(target, "OnChange", func(r Dependable) {
			for _, id := range ids {
				r.AddDependency(id)
			}
		})
	})
}

// WatchChanges arms the change gate on explicit watched resource IDs, the
// ids-level counterpart of OnChange(resources...) without the ordering
// dependency. Plan handlers use it (through RecordedChangeGate) to rebuild
// a recorded gate on the destination, where ordering is already handled by
// the plan engine's dep sort; recipes should prefer OnChange. An empty
// watch list can never fire, so it is registration-time misuse.
func WatchChanges(ids ...string) changeGateOption {
	gate := changeGate("WatchChanges", ids)
	return changeGateOption(func(target any) {
		if len(ids) == 0 {
			logger.Fatal("WatchChanges requires at least one resource id to watch")
		}
		gate(target)
	})
}

// RecordedChangeGate rebuilds a recorded plan op's change gate (its
// if_changed and watch fields) as the option that arms it on the rebuilt
// resource. It is the one apply-side rule shared by every gated kind's plan
// handler: an ungated op yields a nil option (nothing to append), and a
// gated op with no watch ids is an error naming kind, since its gate could
// never fire. That error is defence in depth: plan.Apply (and every
// controller-side pre-flight) refuses such ops earlier, in
// ValidateChangeGates, so no plan reaches a handler with one.
func RecordedChangeGate(kind string, ifChanged bool, watch []string) (ChangeGateOption, error) {
	if !ifChanged {
		return nil, nil
	}
	if len(watch) == 0 {
		return nil, fmt.Errorf("%s: if_changed without watch ids", kind)
	}
	return WatchChanges(watch...), nil
}

// WithCronUser sets the account whose crontab is managed.
func WithCronUser(user string) cronOption {
	return cronOption(func(target any) {
		requires(target, "WithCronUser", func(r CronUserable) { r.SetCronUser(user) })
	})
}

// WithLegacyCommand opts into adopting one unmanaged cron line whose parsed
// command is exactly command. It never matches command substrings or lines in
// a Gonf-managed block. Gonf serializes its own crontab updates, but callers
// must ensure external crontab writers do not run concurrently.
func WithLegacyCommand(command string) cronOption {
	return cronOption(func(target any) {
		requires(target, "WithLegacyCommand", func(r LegacyCronCommandable) { r.SetLegacyCommand(command) })
	})
}

// WithCommand sets a cron command or systemd timer command.
func WithCommand(command string) cronSystemdTimerOption {
	return cronSystemdTimerOption(func(target any) {
		requires(target, "WithCommand", func(r Commandable) { r.SetCommand(command) })
	})
}

// WithMinute sets the cron minute field.
func WithMinute(value string) cronOption {
	return cronValue("WithMinute", value, func(r Minuteable, v string) { r.SetMinute(v) })
}

// WithHour sets the cron hour field.
func WithHour(value string) cronOption {
	return cronValue("WithHour", value, func(r Hourable, v string) { r.SetHour(v) })
}

// WithMonthday sets the cron day-of-month field.
func WithMonthday(value string) cronOption {
	return cronValue("WithMonthday", value, func(r Monthdayable, v string) { r.SetMonthday(v) })
}

// WithMonth sets the cron month field.
func WithMonth(value string) cronOption {
	return cronValue("WithMonth", value, func(r Monthable, v string) { r.SetMonth(v) })
}

// WithWeekday sets the cron day-of-week field.
func WithWeekday(value string) cronOption {
	return cronValue("WithWeekday", value, func(r Weekdayable, v string) { r.SetWeekday(v) })
}

// WithCronEnv adds an environment assignment above a cron entry.
func WithCronEnv(kv string) cronOption {
	return cronOption(func(target any) {
		requires(target, "WithCronEnv", func(r CronEnvable) { r.AddCronEnv(kv) })
	})
}

// WithOnCalendar sets a systemd timer's calendar expression.
func WithOnCalendar(value string) systemdTimerOption {
	return systemdTimerOption(func(target any) {
		requires(target, "WithOnCalendar", func(r OnCalendarable) { r.SetOnCalendar(value) })
	})
}

// WithOnBootSec sets a systemd timer's boot delay.
func WithOnBootSec(value string) systemdTimerOption {
	return systemdTimerOption(func(target any) {
		requires(target, "WithOnBootSec", func(r OnBootSecable) { r.SetOnBootSec(value) })
	})
}

// WithDescription sets the systemd timer unit description.
func WithDescription(value string) systemdTimerOption {
	return systemdTimerOption(func(target any) {
		requires(target, "WithDescription", func(r Descriptionable) { r.SetDescription(value) })
	})
}

// WithServiceDescription sets the companion service description.
func WithServiceDescription(value string) systemdTimerOption {
	return systemdTimerOption(func(target any) {
		requires(target, "WithServiceDescription", func(r ServiceDescriptionable) { r.SetServiceDescription(value) })
	})
}

// WithAfter appends systemd service ordering dependencies.
func WithAfter(units ...string) systemdTimerOption {
	return systemdTimerOption(func(target any) {
		requires(target, "WithAfter", func(r Afterable) { r.AddAfter(units...) })
	})
}

// WithWants appends systemd service wanted dependencies.
func WithWants(units ...string) systemdTimerOption {
	return systemdTimerOption(func(target any) {
		requires(target, "WithWants", func(r Wantsable) { r.AddWants(units...) })
	})
}

// WithSymlink makes a link resource symbolic.
func WithSymlink(target string) linkOption {
	return linkOption(func(value any) {
		requires(value, "WithSymlink", func(r Linkable) { r.SetSymlink(target) })
	})
}

// WithHardlink makes a link resource hard-linked.
func WithHardlink(target string) linkOption {
	return linkOption(func(value any) {
		requires(value, "WithHardlink", func(r Linkable) { r.SetHardlink(target) })
	})
}

// WithName overrides a command's registry name or a file resource's
// identity. A named File still manages its supplied path, but uses
// File[name] for registration, dependencies, and change reports. This lets
// separate declarations safely manage distinct line edits to one path.
func WithName(name string) fileCommandOption {
	return fileCommandOption(func(target any) {
		requires(target, "WithName", func(r Named) { r.SetName(name) })
	})
}

// WithDir sets a command's working directory.
func WithDir(dir string) commandOption {
	return commandOption(func(target any) {
		requires(target, "WithDir", func(r Dirable) { r.SetDir(dir) })
	})
}

// WithEnv merges extra environment variables into a command or package
// operation. Package managers receive the variables for both state probes and
// mutations, which allows declarative custom repository configuration such as
// PKG_PATH for OpenBSD pkg_add.
//
// The map is copied when the option is applied to a resource, so mutating or
// reusing it afterwards has no effect on that resource, its plan draft or its
// recorded plan op. Every Envable setter must honour this copy contract.
func WithEnv(env map[string]string) packageCommandOption {
	return packageCommandOption(func(target any) {
		requires(target, "WithEnv", func(r Envable) { r.SetEnv(env) })
	})
}

// Creates skips a command when path already exists.
func Creates(path string) commandOption {
	return commandOption(func(target any) {
		requires(target, "Creates", func(r Creatable) { r.SetCreates(path) })
	})
}

// Unless skips a command when the guard probe succeeds.
func Unless(name string, args []string, opts ...GuardOption) commandOption {
	return commandOption(func(target any) {
		requires(target, "Unless", func(r Guardable) { r.SetUnless(newGuard(name, args, opts...)) })
	})
}

// OnlyIf runs a command only when the guard probe succeeds.
func OnlyIf(name string, args []string, opts ...GuardOption) commandOption {
	return commandOption(func(target any) {
		requires(target, "OnlyIf", func(r Guardable) { r.SetOnlyIf(newGuard(name, args, opts...)) })
	})
}

// Marker methods: each empty method admits the option type into one resource
// family interface (fileOption into FileOption, and so on).
func (allResourceOption) fileOption()              {}
func (allResourceOption) dirOption()               {}
func (allResourceOption) linkOption()              {}
func (allResourceOption) packageOption()           {}
func (allResourceOption) serviceOption()           {}
func (allResourceOption) cronOption()              {}
func (allResourceOption) timerOption()             {}
func (allResourceOption) systemdTimerOption()      {}
func (allResourceOption) daemonReloadOption()      {}
func (allResourceOption) commandOption()           {}
func (allResourceOption) localUserOption()         {}
func (fileDirOption) fileOption()                  {}
func (fileDirOption) dirOption()                   {}
func (fileOption) fileOption()                     {}
func (fileCommandOption) fileOption()              {}
func (fileCommandOption) commandOption()           {}
func (dirOption) dirOption()                       {}
func (linkOption) linkOption()                     {}
func (packageOption) packageOption()               {}
func (packageCommandOption) packageOption()        {}
func (packageCommandOption) commandOption()        {}
func (serviceOption) serviceOption()               {}
func (cronOption) cronOption()                     {}
func (systemdTimerOption) systemdTimerOption()     {}
func (daemonReloadOption) daemonReloadOption()     {}
func (commandOption) commandOption()               {}
func (groupOption) fileOption()                    {}
func (groupOption) dirOption()                     {}
func (groupOption) localUserOption()               {}
func (userAccountOption) localUserOption()         {}
func (absentOption) fileOption()                   {}
func (absentOption) dirOption()                    {}
func (absentOption) linkOption()                   {}
func (absentOption) packageOption()                {}
func (absentOption) serviceOption()                {}
func (absentOption) cronOption()                   {}
func (absentOption) timerOption()                  {}
func (absentOption) systemdTimerOption()           {}
func (serviceTimerOption) serviceOption()          {}
func (serviceTimerOption) timerOption()            {}
func (serviceTimerOption) systemdTimerOption()     {}
func (userOption) serviceOption()                  {}
func (userOption) timerOption()                    {}
func (userOption) systemdTimerOption()             {}
func (userOption) daemonReloadOption()             {}
func (enableOnlyOption) timerOption()              {}
func (enableOnlyOption) systemdTimerOption()       {}
func (cronSystemdTimerOption) cronOption()         {}
func (cronSystemdTimerOption) systemdTimerOption() {}
func (changeGateOption) commandOption()            {}
func (changeGateOption) serviceOption()            {}
func (changeGateOption) timerOption()              {}
func (changeGateOption) daemonReloadOption()       {}

// toLegacyOptions wraps every erased option with legacy, the family-specific
// conversion used by the To*Options adapters. It performs no validation.
func toLegacyOptions[T any](opts []Option, legacy func(func(any)) T) []T {
	out := make([]T, 0, len(opts))
	for _, option := range opts {
		out = append(out, legacy(option))
	}
	return out
}

// cronValue builds the cron option for one schedule field: label names the
// option in misuse errors and set stores value through capability T.
func cronValue[T any](label, value string, set func(T, string)) cronOption {
	return cronOption(func(target any) { requires(target, label, func(r T) { set(r, value) }) })
}

// changeGate is the single lowering of every change-gate option: it arms
// target's gate and adds ids (nil arms only) through the one
// ChangeWatchable capability. label names the option in misuse errors. The
// ids are copied once, so a recipe mutating its slice afterwards cannot
// change what the option records.
func changeGate(label string, ids []string) func(any) {
	ids = slices.Clone(ids)
	return func(target any) {
		requires(target, label, func(r ChangeWatchable) { r.SetChangeWatch(ids) })
	}
}

// legacyChangeGate is changeGate for the legacy daemon-reload spellings
// (IfChanged, WithWatch): target must also be ChangeGated, so the
// type-erased Option path refuses them on resources without the DependsOn
// fallback exactly as before the families were collapsed.
func legacyChangeGate(label string, ids []string) func(any) {
	gate := changeGate(label, ids)
	return func(target any) {
		requires(target, label, func(ChangeGated) {})
		gate(target)
	}
}

// requires asserts that target implements capability T and calls use with it.
// A target lacking T is recipe misuse, so it aborts through logger.Fatal
// naming the target type and the option label.
func requires[T any](target any, label string, use func(T)) {
	r, ok := target.(T)
	if !ok {
		logger.Fatal("%T does not support %s", target, label)
	}
	use(r)
}
