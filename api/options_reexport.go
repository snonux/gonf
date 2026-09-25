// Code in this file re-exports the resource options into package api, so a
// recipe needs a single dot import:
//
//	import . "github.com/snonux/gonf/api"
//
// Every declaration is an alias of the same-named resource/options
// identifier, whose doc comment is authoritative. api/options keeps the
// same aliases for older recipes; do not dot-import both packages (Go
// rejects the duplicate names). TestOptionsSurfaceMatchesAPIOptions keeps
// this list in step with api/options.

package api

import (
	"os"

	"github.com/snonux/gonf/resource"
	resourceoptions "github.com/snonux/gonf/resource/options"
)

// Re-exported from api/options/option.go.

// Core option types: the erased Option function and the Guard used by the
// command guards Unless and OnlyIf.
type (
	Option      = resourceoptions.Option
	Guard       = resourceoptions.Guard
	GuardOption = resourceoptions.GuardOption
)

// Capability interfaces. A resource implements one small setter interface per
// option it accepts. The family types below normally stop a mismatch at
// compile time; an erased Option applied to a resource lacking the capability
// is reported as recipe misuse when the option is applied (a declaration
// error that fails the record; the option does nothing).
type (
	Owner                  = resourceoptions.Owner
	Grouped                = resourceoptions.Grouped
	Moded                  = resourceoptions.Moded
	Sourced                = resourceoptions.Sourced
	SourceGlobable         = resourceoptions.SourceGlobable
	SourceBaseable         = resourceoptions.SourceBaseable
	Paramable              = resourceoptions.Paramable
	Templateable           = resourceoptions.Templateable
	TemplateDataable       = resourceoptions.TemplateDataable
	Validatable            = resourceoptions.Validatable
	Contented              = resourceoptions.Contented
	LineAddable            = resourceoptions.LineAddable
	LineRemovable          = resourceoptions.LineRemovable
	LinesAddable           = resourceoptions.LinesAddable
	LinesRemovable         = resourceoptions.LinesRemovable
	KeyedLineSettable      = resourceoptions.KeyedLineSettable
	FileModed              = resourceoptions.FileModed
	Prunable               = resourceoptions.Prunable
	Absentable             = resourceoptions.Absentable
	Latestable             = resourceoptions.Latestable
	Dependable             = resourceoptions.Dependable
	Named                  = resourceoptions.Named
	Dirable                = resourceoptions.Dirable
	Envable                = resourceoptions.Envable
	Creatable              = resourceoptions.Creatable
	Guardable              = resourceoptions.Guardable
	Linkable               = resourceoptions.Linkable
	Restartable            = resourceoptions.Restartable
	Reloadable             = resourceoptions.Reloadable
	UserService            = resourceoptions.UserService
	EnableOnlyable         = resourceoptions.EnableOnlyable
	ChangeGated            = resourceoptions.ChangeGated
	Watchable              = resourceoptions.Watchable
	ChangeWatchable        = resourceoptions.ChangeWatchable
	Elevatable             = resourceoptions.Elevatable
	CronUserable           = resourceoptions.CronUserable
	LegacyCronCommandable  = resourceoptions.LegacyCronCommandable
	Commandable            = resourceoptions.Commandable
	Minuteable             = resourceoptions.Minuteable
	Hourable               = resourceoptions.Hourable
	Monthdayable           = resourceoptions.Monthdayable
	Monthable              = resourceoptions.Monthable
	Weekdayable            = resourceoptions.Weekdayable
	CronEnvable            = resourceoptions.CronEnvable
	Scheduleable           = resourceoptions.Scheduleable
	Flaggable              = resourceoptions.Flaggable
	Homeable               = resourceoptions.Homeable
	CreateHomeable         = resourceoptions.CreateHomeable
	Shellable              = resourceoptions.Shellable
	Classable              = resourceoptions.Classable
	Systemable             = resourceoptions.Systemable
	SupplementaryGroupable = resourceoptions.SupplementaryGroupable
	OnCalendarable         = resourceoptions.OnCalendarable
	OnBootSecable          = resourceoptions.OnBootSecable
	Persistentable         = resourceoptions.Persistentable
	Descriptionable        = resourceoptions.Descriptionable
	ServiceDescriptionable = resourceoptions.ServiceDescriptionable
	Afterable              = resourceoptions.Afterable
	Wantsable              = resourceoptions.Wantsable
	MisuseReporter         = resourceoptions.MisuseReporter
)

// Resource-family option types. Each constructor accepts only its family
// (e.g. api.File takes FileOption), so a mismatched option is a compile-time
// error; the shared families (AllResourceOption, FileDirOption, ...) are the
// types of options valid on several resources at once.
type (
	FileOption             = resourceoptions.FileOption
	DirOption              = resourceoptions.DirOption
	LinkOption             = resourceoptions.LinkOption
	PackageOption          = resourceoptions.PackageOption
	ServiceOption          = resourceoptions.ServiceOption
	CronOption             = resourceoptions.CronOption
	TimerOption            = resourceoptions.TimerOption
	SystemdTimerOption     = resourceoptions.SystemdTimerOption
	DaemonReloadOption     = resourceoptions.DaemonReloadOption
	CommandOption          = resourceoptions.CommandOption
	AllResourceOption      = resourceoptions.AllResourceOption
	FileDirOption          = resourceoptions.FileDirOption
	AbsentOption           = resourceoptions.AbsentOption
	ServiceTimerOption     = resourceoptions.ServiceTimerOption
	UserOption             = resourceoptions.UserOption
	LocalUserOption        = resourceoptions.LocalUserOption
	EnableOnlyOption       = resourceoptions.EnableOnlyOption
	CronSystemdTimerOption = resourceoptions.CronSystemdTimerOption
	ChangeGateOption       = resourceoptions.ChangeGateOption
)

// Dependency ordering (every resource) and the command guard matchers.
var (
	DependsOn    = resourceoptions.DependsOn
	ExpectExit   = resourceoptions.ExpectExit
	ExpectStdout = resourceoptions.ExpectStdout
)

// Ownership and user-account options (files, directories, local users).
var (
	WithOwner               = resourceoptions.WithOwner
	WithGroup               = resourceoptions.WithGroup
	WithHome                = resourceoptions.WithHome
	WithCreateHome          = resourceoptions.WithCreateHome
	WithShell               = resourceoptions.WithShell
	WithClass               = resourceoptions.WithClass
	WithLoginClass          = resourceoptions.WithLoginClass
	WithPrimaryGroup        = resourceoptions.WithPrimaryGroup
	WithUserGroup           = resourceoptions.WithUserGroup
	WithSystem              = resourceoptions.WithSystem
	WithSupplementaryGroups = resourceoptions.WithSupplementaryGroups
)

// File and directory content options: mode, sources, templates, validation,
// inline content, line edits and directory pruning.
var (
	WithMode         = resourceoptions.WithMode
	Perm             = resourceoptions.Perm
	WithSource       = resourceoptions.WithSource
	WithSourceGlob   = resourceoptions.WithSourceGlob
	WithParam        = resourceoptions.WithParam
	WithTemplate     = resourceoptions.WithTemplate
	WithTemplateData = resourceoptions.WithTemplateData
	WithValidation   = resourceoptions.WithValidation
	WithSourceBase   = resourceoptions.WithSourceBase
	WithContent      = resourceoptions.WithContent
	WithLines        = resourceoptions.WithLines
	WithoutLines     = resourceoptions.WithoutLines
	WithLine         = resourceoptions.WithLine
	WithoutLine      = resourceoptions.WithoutLine
	WithKeyedLine    = resourceoptions.WithKeyedLine
	WithFileMode     = resourceoptions.WithFileMode
	WithPrune        = resourceoptions.WithPrune
)

// State options (absence, latest package), service/timer behaviour and the
// one change-gate family: OnChange/WatchChanges (also accepted by commands)
// plus the legacy daemon-reload spellings IfChanged/WithWatch, which feed
// the same gate and watch list.
var (
	IsAbsent       = resourceoptions.IsAbsent
	IsLatest       = resourceoptions.IsLatest
	WithRestart    = resourceoptions.WithRestart
	WithReload     = resourceoptions.WithReload
	WithFlags      = resourceoptions.WithFlags
	WithUser       = resourceoptions.WithUser
	WithEnableOnly = resourceoptions.WithEnableOnly
	IfChanged      = resourceoptions.IfChanged
	WithWatch      = resourceoptions.WithWatch
	OnChange       = resourceoptions.OnChange
	WatchChanges   = resourceoptions.WatchChanges
)

// Cron schedule and environment options (WithCommand is shared with systemd
// timers).
var (
	WithCronUser      = resourceoptions.WithCronUser
	WithLegacyCommand = resourceoptions.WithLegacyCommand
	WithCommand       = resourceoptions.WithCommand
	WithMinute        = resourceoptions.WithMinute
	WithHour          = resourceoptions.WithHour
	WithMonthday      = resourceoptions.WithMonthday
	WithMonth         = resourceoptions.WithMonth
	WithWeekday       = resourceoptions.WithWeekday
	WithCronEnv       = resourceoptions.WithCronEnv
	WithSchedule      = resourceoptions.WithSchedule
)

// Systemd timer unit options.
var (
	WithOnCalendar         = resourceoptions.WithOnCalendar
	WithOnBootSec          = resourceoptions.WithOnBootSec
	WithPersistent         = resourceoptions.WithPersistent
	WithDescription        = resourceoptions.WithDescription
	WithServiceDescription = resourceoptions.WithServiceDescription
	WithAfter              = resourceoptions.WithAfter
	WithWants              = resourceoptions.WithWants
)

// Link targets, and command options: privilege elevation, name (also a
// File's registry identity), working directory, environment (also accepted by
// packages; WithEnv copies its map when the option is applied to a
// resource, so mutations after that have no effect), and the
// Creates/Unless/OnlyIf guards.
var (
	WithSymlink  = resourceoptions.WithSymlink
	WithHardlink = resourceoptions.WithHardlink
	WithElevate  = resourceoptions.WithElevate
	WithName     = resourceoptions.WithName
	WithDir      = resourceoptions.WithDir
	WithEnv      = resourceoptions.WithEnv
	Creates      = resourceoptions.Creates
	Unless       = resourceoptions.Unless
	OnlyIf       = resourceoptions.OnlyIf
)

// Adapters from an erased []Option slice to one resource family's option
// slice, for callers that still build options generically.
var (
	ToFileOptions         = resourceoptions.ToFileOptions
	ToDirOptions          = resourceoptions.ToDirOptions
	ToLinkOptions         = resourceoptions.ToLinkOptions
	ToPackageOptions      = resourceoptions.ToPackageOptions
	ToServiceOptions      = resourceoptions.ToServiceOptions
	ToCronOptions         = resourceoptions.ToCronOptions
	ToTimerOptions        = resourceoptions.ToTimerOptions
	ToSystemdTimerOptions = resourceoptions.ToSystemdTimerOptions
	ToDaemonReloadOptions = resourceoptions.ToDaemonReloadOptions
	ToCommandOptions      = resourceoptions.ToCommandOptions
	ToLocalUserOptions    = resourceoptions.ToLocalUserOptions
)

// CandidatePath is the sole placeholder allowed in WithValidation arguments;
// it is replaced at apply time with the private staged candidate's path.
const CandidatePath = resourceoptions.CandidatePath

// Root is the owner spec for root and the destination OS's root group, for
// Perm and WithOwner.
const Root = resourceoptions.Root

// Re-exported from api/options/configset.go.

// ConfigSetOption configures api.ConfigSet. See resource/options for the
// full contract and docs/design/config-set.md for the publication guarantees.
type ConfigSetOption = resourceoptions.ConfigSetOption

// Capability interfaces of the config-set options.
type (
	MemberAddable  = resourceoptions.MemberAddable
	SetValidatable = resourceoptions.SetValidatable
	Chrootable     = resourceoptions.Chrootable
	StagingDirable = resourceoptions.StagingDirable
)

// Member-path token parts; recipes use MemberPath and MemberChrootPath.
const (
	MemberPathTokenPrefix       = resourceoptions.MemberPathTokenPrefix
	MemberChrootPathTokenPrefix = resourceoptions.MemberChrootPathTokenPrefix
	MemberTokenSuffix           = resourceoptions.MemberTokenSuffix
)

// Config-set options and member-path placeholders.
var (
	// ConfigFile adds one member file to a config set.
	ConfigFile = resourceoptions.ConfigFile
	// WithSetValidation adds an argv validator run against the complete
	// staged set before any member is published.
	WithSetValidation = resourceoptions.WithSetValidation
	// WithChroot declares the daemon's chroot; members and staging must lie
	// below it and MemberChrootPath renders relative to it.
	WithChroot = resourceoptions.WithChroot
	// WithStagingDir selects the directory receiving the private staging
	// directory (default: the members' deepest common directory).
	WithStagingDir = resourceoptions.WithStagingDir
	// MemberPath is the typed placeholder for a member's absolute path
	// (staged while validating, live when published).
	MemberPath = resourceoptions.MemberPath
	// MemberChrootPath is MemberPath relative to the WithChroot directory.
	MemberChrootPath = resourceoptions.MemberChrootPath
)

// Re-exported from api/options/sensitive.go.

// Explicit sensitivity (see resource/options WithSensitive and
// docs/design/secrets.md): the option, the family set that accepts it (File, Dir,
// Package, Cron, SystemdTimer, Command, ConfigSet and ConfigFile members)
// and its capability interface.
type (
	SensitiveOption = resourceoptions.SensitiveOption
	Sensitivable    = resourceoptions.Sensitivable
)

// WithSensitive marks a resource's payload as secret material the plan's
// secret scan cannot recognise (transformed values, synced trees).
var WithSensitive = resourceoptions.WithSensitive

// Re-exported from api/options/user.go.

// HomeManageable is implemented by resources accepting WithManageHome.
type HomeManageable = resourceoptions.HomeManageable

// WithManageHome opts a User in to converging an existing account's passwd
// home field to WithHome, without moving or creating the directory. See
// resource/options.WithManageHome and docs/design/user.md for the full contract.
var WithManageHome = resourceoptions.WithManageHome

// ModeToFlags converts raw octal special bits into Go FileMode flags.
func ModeToFlags(mode os.FileMode) os.FileMode { return resourceoptions.ModeToFlags(mode) }

// ModeToWire renders a mode in the canonical plan-wire format.
func ModeToWire(mode os.FileMode) string { return resourceoptions.ModeToWire(mode) }

// Dependency is anything a resource option such as DependsOn or OnChange can
// point at (a declared Resource); recipes build []Dependency slices from it.
type Dependency = resource.Dependency

// Refuse records a declaration that cannot be built (for example a template
// that failed to render) as a refused resource of kind and id, so the plan
// fails with err instead of declaring a resource with empty content.
var Refuse = resource.Refuse
