// Package options provides interface-based, resource-agnostic configuration
// options shared by concrete resource packages (file, dir, link, pkg, service,
// timer, cron, cmd, …).
package options

import (
	"os"

	"github.com/snonux/gonf/internal/logger"
	"github.com/snonux/gonf/resource"
)

// Option configures a resource. It is applied to the concrete resource value
// (e.g. *file.File) during construction.
type Option func(any)

// Capability interfaces. A resource implements only the setters it supports.
type (
	Owner          interface{ SetOwner(string) }
	Grouped        interface{ SetGroup(string) }
	Moded          interface{ SetMode(os.FileMode) }
	Sourced        interface{ SetSource(string) }
	SourceGlobable interface{ SetSourceGlob(string) }
	SourceBaseable interface{ SetSourceBase(string) }
	Paramable      interface{ SetParam(string) }
	Contented      interface{ SetContent(string) }
	LineAddable    interface{ SetAddLine(string) }
	LineRemovable  interface{ SetRemoveLine(string) }
	FileModed      interface{ SetFileMode(os.FileMode) }
	Prunable       interface{ SetPrune() }
	Absentable     interface{ SetAbsent() }
	Latestable     interface{ SetLatest() }
	Dependable     interface{ AddDependency(id string) }
	Named          interface{ SetName(string) }
	Dirable        interface{ SetDir(string) }
	Envable        interface{ SetEnv(map[string]string) }
	Creatable      interface{ SetCreates(string) }
	Guardable      interface {
		SetUnless(*Guard)
		SetOnlyIf(*Guard)
	}
	Linkable interface {
		SetSymlink(target string)
		SetHardlink(target string)
	}
	Restartable    interface{ SetRestart() }
	Reloadable     interface{ SetReload() }
	UserService    interface{ SetUser() }
	EnableOnlyable interface{ SetEnableOnly() }
	ChangeGated    interface{ SetIfChanged() }
	Watchable      interface{ SetWatch([]string) }
	Elevatable     interface{ SetElevate() }
	CronUserable   interface{ SetCronUser(string) }
	Commandable    interface{ SetCommand(string) }
	Minuteable     interface{ SetMinute(string) }
	Hourable       interface{ SetHour(string) }
	Monthdayable   interface{ SetMonthday(string) }
	Monthable      interface{ SetMonth(string) }
	Weekdayable    interface{ SetWeekday(string) }
	CronEnvable    interface{ AddCronEnv(string) }

	OnCalendarable         interface{ SetOnCalendar(string) }
	OnBootSecable          interface{ SetOnBootSec(string) }
	Persistentable         interface{ SetPersistent() }
	Descriptionable        interface{ SetDescription(string) }
	ServiceDescriptionable interface{ SetServiceDescription(string) }
	Afterable              interface{ AddAfter(...string) }
	Wantsable              interface{ AddWants(...string) }
)

// Guard describes an Unless/OnlyIf probe: run Name with Args and treat the
// probe as successful when the exit code matches ExpectExit and, if
// ExpectStdout is non-empty, when trimmed stdout equals that string.
type Guard struct {
	Name         string
	Args         []string
	ExpectExit   int
	ExpectStdout string
}

// GuardOption configures a Guard built by Unless or OnlyIf.
type GuardOption func(*Guard)

// ExpectExit sets the exit code that makes a guard succeed (default 0).
func ExpectExit(code int) GuardOption {
	return func(g *Guard) { g.ExpectExit = code }
}

// ExpectStdout requires trimmed stdout to equal want for the guard to succeed.
func ExpectStdout(want string) GuardOption {
	return func(g *Guard) { g.ExpectStdout = want }
}

// DependsOn declares that the resource being configured must be applied only
// after every given resource has been applied. Each argument may be a single
// resource or a Multi; a Multi is expanded so the dependency is recorded for
// each of its members individually.
func DependsOn(deps ...resource.Dependency) Option {
	return func(t any) {
		requires(t, "DependsOn", func(r Dependable) {
			for _, dep := range deps {
				for _, id := range dep.Dependencies() {
					r.AddDependency(id)
				}
			}
		})
	}
}

// WithOwner sets the owning user of the resource.
func WithOwner(owner string) Option {
	return func(t any) {
		requires(t, "WithOwner", func(r Owner) { r.SetOwner(owner) })
	}
}

// WithGroup sets the owning group of the resource.
func WithGroup(group string) Option {
	return func(t any) {
		requires(t, "WithGroup", func(r Grouped) { r.SetGroup(group) })
	}
}

// WithMode sets the resource's own file mode. Both raw octal literals with
// special bits (0o4755-style setuid/setgid/sticky) and Go flag-style FileModes
// (0o755|os.ModeSetuid) are accepted; normalizeMode converts both to the same
// flag-form FileMode. Bits above 0o7777 are a programmer error and abort via
// logger.Fatal.
func WithMode(mode os.FileMode) Option {
	return func(t any) {
		requires(t, "WithMode", func(r Moded) { r.SetMode(normalizeMode(mode)) })
	}
}

// WithSource sets the source path the resource is populated from.
func WithSource(source string) Option {
	return func(t any) {
		requires(t, "WithSource", func(r Sourced) { r.SetSource(source) })
	}
}

// WithSourceGlob copies regular files matching pattern into a directory as
// basename entries (flat install). Mutually exclusive with WithSource.
func WithSourceGlob(pattern string) Option {
	return func(t any) {
		requires(t, "WithSourceGlob", func(r SourceGlobable) { r.SetSourceGlob(pattern) })
	}
}

// WithParam overrides the {{.Param}} value rendered into template content.
// The default stays the resource's derived value (the bare source path, or
// the literal content); plan apply uses the override to give source-tree
// template copies a stable identity (the recipe's declared source directory
// plus the entry's relative path) instead of the ephemeral blob-extraction
// path, which changes every plan run.
func WithParam(value string) Option {
	return func(t any) {
		requires(t, "WithParam", func(r Paramable) { r.SetParam(value) })
	}
}

// WithSourceBase sets the declared source directory for a dir resource that
// syncs a packaged blob tree: .tmpl entries inside the tree render
// {{.Param}} as sourceBase plus the entry's path relative to the tree root.
// Recipe code does not use it; it is the plan engine's plumbing so synced
// templates do not embed the per-run blob-extraction path.
func WithSourceBase(value string) Option {
	return func(t any) {
		requires(t, "WithSourceBase", func(r SourceBaseable) { r.SetSourceBase(value) })
	}
}

// WithContent sets literal content for the resource.
func WithContent(content string) Option {
	return func(t any) {
		requires(t, "WithContent", func(r Contented) { r.SetContent(content) })
	}
}

// WithLine appends a line of content to the resource.
func WithLine(content string) Option {
	return func(t any) {
		requires(t, "WithLine", func(r LineAddable) { r.SetAddLine(content) })
	}
}

// WithoutLine removes a line of content from the resource.
func WithoutLine(content string) Option {
	return func(t any) {
		requires(t, "WithoutLine", func(r LineRemovable) { r.SetRemoveLine(content) })
	}
}

// WithFileMode sets the mode applied to regular files copied from a source
// tree (distinct from the resource's own mode). Accepts the same dual form as
// WithMode: raw octal special bits or Go flag-style FileModes.
func WithFileMode(mode os.FileMode) Option {
	return func(t any) {
		requires(t, "WithFileMode", func(r FileModed) { r.SetFileMode(normalizeMode(mode)) })
	}
}

// WithPrune enables reconciliation of extra destination entries during a
// source copy, and recursive removal during IsAbsent().
var WithPrune Option = func(t any) {
	requires(t, "WithPrune", func(r Prunable) { r.SetPrune() })
}

// IsAbsent marks the resource for removal.
var IsAbsent Option = func(t any) {
	requires(t, "IsAbsent", func(r Absentable) { r.SetAbsent() })
}

// IsLatest marks the package resource to be updated to the latest version.
var IsLatest Option = func(t any) {
	requires(t, "IsLatest", func(r Latestable) { r.SetLatest() })
}

// WithRestart restarts the service or timer once during this apply after
// converging to the desired running/active state.
var WithRestart Option = func(t any) {
	requires(t, "WithRestart", func(r Restartable) { r.SetRestart() })
}

// WithReload reloads the service once during this apply when the backend
// supports a reload action. Takes precedence over WithRestart when both are set.
// Timer resources do not support WithReload.
var WithReload Option = func(t any) {
	requires(t, "WithReload", func(r Reloadable) { r.SetReload() })
}

// WithUser selects the systemd user bus (--user). Only valid on systemd.
var WithUser Option = func(t any) {
	requires(t, "WithUser", func(r UserService) { r.SetUser() })
}

// WithElevate marks a command (or other Elevatable) so its plan op has
// elevate=true even inside an unprivileged task.
var WithElevate Option = func(t any) {
	requires(t, "WithElevate", func(r Elevatable) { r.SetElevate() })
}

// WithEnableOnly makes Timer converge enable/disable without start/stop.
var WithEnableOnly Option = func(t any) {
	requires(t, "WithEnableOnly", func(r EnableOnlyable) { r.SetEnableOnly() })
}

// IfChanged skips DaemonReload unless a DependsOn (or WithWatch) target was
// noted StatusChanged / StatusWouldChange. Directory deps also see child File notes.
var IfChanged Option = func(t any) {
	requires(t, "IfChanged", func(r ChangeGated) { r.SetIfChanged() })
}

// WithWatch sets the resource ids IfChanged consults (plan apply / Ensure).
func WithWatch(ids ...string) Option {
	return func(t any) {
		requires(t, "WithWatch", func(r Watchable) { r.SetWatch(ids) })
	}
}

// WithCronUser sets the account whose crontab is managed (default "root").
func WithCronUser(user string) Option {
	return func(t any) {
		requires(t, "WithCronUser", func(r CronUserable) { r.SetCronUser(user) })
	}
}

// WithCommand sets the command line for a Cron resource (Puppet-inspired).
func WithCommand(cmd string) Option {
	return func(t any) {
		requires(t, "WithCommand", func(r Commandable) { r.SetCommand(cmd) })
	}
}

// WithMinute sets the cron minute field (default "*").
func WithMinute(v string) Option {
	return func(t any) {
		requires(t, "WithMinute", func(r Minuteable) { r.SetMinute(v) })
	}
}

// WithHour sets the cron hour field (default "*").
func WithHour(v string) Option {
	return func(t any) {
		requires(t, "WithHour", func(r Hourable) { r.SetHour(v) })
	}
}

// WithMonthday sets the cron day-of-month field (default "*").
func WithMonthday(v string) Option {
	return func(t any) {
		requires(t, "WithMonthday", func(r Monthdayable) { r.SetMonthday(v) })
	}
}

// WithMonth sets the cron month field (default "*").
func WithMonth(v string) Option {
	return func(t any) {
		requires(t, "WithMonth", func(r Monthable) { r.SetMonth(v) })
	}
}

// WithWeekday sets the cron day-of-week field (default "*").
func WithWeekday(v string) Option {
	return func(t any) {
		requires(t, "WithWeekday", func(r Weekdayable) { r.SetWeekday(v) })
	}
}

// WithCronEnv appends an environment assignment (KEY=VAL) above the cron line.
func WithCronEnv(kv string) Option {
	return func(t any) {
		requires(t, "WithCronEnv", func(r CronEnvable) { r.AddCronEnv(kv) })
	}
}

// WithOnCalendar sets the systemd timer OnCalendar= expression (required for
// a present SystemdTimer).
func WithOnCalendar(v string) Option {
	return func(t any) {
		requires(t, "WithOnCalendar", func(r OnCalendarable) { r.SetOnCalendar(v) })
	}
}

// WithOnBootSec sets the systemd timer OnBootSec= delay (optional).
func WithOnBootSec(v string) Option {
	return func(t any) {
		requires(t, "WithOnBootSec", func(r OnBootSecable) { r.SetOnBootSec(v) })
	}
}

// WithPersistent sets Persistent=true on a SystemdTimer unit.
var WithPersistent Option = func(t any) {
	requires(t, "WithPersistent", func(r Persistentable) { r.SetPersistent() })
}

// WithDescription sets the [Unit] Description for a SystemdTimer (.timer;
// also used as the .service Description when WithServiceDescription is unset).
func WithDescription(v string) Option {
	return func(t any) {
		requires(t, "WithDescription", func(r Descriptionable) { r.SetDescription(v) })
	}
}

// WithServiceDescription sets the companion oneshot .service [Unit] Description.
func WithServiceDescription(v string) Option {
	return func(t any) {
		requires(t, "WithServiceDescription", func(r ServiceDescriptionable) { r.SetServiceDescription(v) })
	}
}

// WithAfter appends After= dependencies on the companion oneshot .service.
func WithAfter(units ...string) Option {
	return func(t any) {
		requires(t, "WithAfter", func(r Afterable) { r.AddAfter(units...) })
	}
}

// WithWants appends Wants= dependencies on the companion oneshot .service.
func WithWants(units ...string) Option {
	return func(t any) {
		requires(t, "WithWants", func(r Wantsable) { r.AddWants(units...) })
	}
}

// WithSymlink makes the resource a symbolic link pointing at target.
func WithSymlink(target string) Option {
	return func(t any) {
		requires(t, "WithSymlink", func(r Linkable) { r.SetSymlink(target) })
	}
}

// WithHardlink makes the resource a hard link pointing at target.
func WithHardlink(target string) Option {
	return func(t any) {
		requires(t, "WithHardlink", func(r Linkable) { r.SetHardlink(target) })
	}
}

// WithName overrides the resource's registry name (used in its ID).
func WithName(name string) Option {
	return func(t any) {
		requires(t, "WithName", func(r Named) { r.SetName(name) })
	}
}

// WithDir sets the working directory for a command resource.
func WithDir(dir string) Option {
	return func(t any) {
		requires(t, "WithDir", func(r Dirable) { r.SetDir(dir) })
	}
}

// WithEnv merges extra environment variables into the command's environment.
func WithEnv(env map[string]string) Option {
	return func(t any) {
		requires(t, "WithEnv", func(r Envable) { r.SetEnv(env) })
	}
}

// Creates skips applying the command when path already exists.
func Creates(path string) Option {
	return func(t any) {
		requires(t, "Creates", func(r Creatable) { r.SetCreates(path) })
	}
}

// Unless skips the command when the guard probe succeeds.
func Unless(name string, args []string, opts ...GuardOption) Option {
	return func(t any) {
		requires(t, "Unless", func(r Guardable) { r.SetUnless(newGuard(name, args, opts...)) })
	}
}

// OnlyIf runs the command only when the guard probe succeeds.
func OnlyIf(name string, args []string, opts ...GuardOption) Option {
	return func(t any) {
		requires(t, "OnlyIf", func(r Guardable) { r.SetOnlyIf(newGuard(name, args, opts...)) })
	}
}

// requires asserts that the option target t implements the T capability and
// hands the typed value to use. A mismatch is a programmer error and aborts
// the run via logger.Fatal, e.g. "file.File does not support WithContent".
func requires[T any](t any, label string, use func(T)) {
	r, ok := t.(T)
	if !ok {
		logger.Fatal("%T does not support %s", t, label)
	}
	use(r)
}

func newGuard(name string, args []string, opts ...GuardOption) *Guard {
	g := &Guard{
		Name:       name,
		Args:       args,
		ExpectExit: 0,
	}
	for _, o := range opts {
		o(g)
	}
	return g
}
