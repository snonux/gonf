// Package options provides interface-based, resource-agnostic configuration
// options shared by concrete resource packages (file, dir, link, pkg, service,
// timer, cron, cmd, …).
package options

import (
	"log"
	"os"

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
	Restartable  interface{ SetRestart() }
	Reloadable   interface{ SetReload() }
	UserService  interface{ SetUser() }
	CronUserable interface{ SetCronUser(string) }
	Commandable  interface{ SetCommand(string) }
	Minuteable   interface{ SetMinute(string) }
	Hourable     interface{ SetHour(string) }
	Monthdayable interface{ SetMonthday(string) }
	Monthable    interface{ SetMonth(string) }
	Weekdayable  interface{ SetWeekday(string) }
	CronEnvable  interface{ AddCronEnv(string) }
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
		r, ok := t.(Dependable)
		if !ok {
			log.Fatalf("%T does not support DependsOn", t)
		}
		for _, dep := range deps {
			for _, id := range dep.Dependencies() {
				r.AddDependency(id)
			}
		}
	}
}

// WithOwner sets the owning user of the resource.
func WithOwner(owner string) Option {
	return func(t any) {
		r, ok := t.(Owner)
		if !ok {
			log.Fatalf("%T does not support WithOwner", t)
		}
		r.SetOwner(owner)
	}
}

// WithGroup sets the owning group of the resource.
func WithGroup(group string) Option {
	return func(t any) {
		r, ok := t.(Grouped)
		if !ok {
			log.Fatalf("%T does not support WithGroup", t)
		}
		r.SetGroup(group)
	}
}

// WithMode sets the resource's own file mode.
func WithMode(mode os.FileMode) Option {
	return func(t any) {
		r, ok := t.(Moded)
		if !ok {
			log.Fatalf("%T does not support WithMode", t)
		}
		r.SetMode(mode)
	}
}

// WithSource sets the source path the resource is populated from.
func WithSource(source string) Option {
	return func(t any) {
		r, ok := t.(Sourced)
		if !ok {
			log.Fatalf("%T does not support WithSource", t)
		}
		r.SetSource(source)
	}
}

// WithSourceGlob copies regular files matching pattern into a directory as
// basename entries (flat install). Mutually exclusive with WithSource.
func WithSourceGlob(pattern string) Option {
	return func(t any) {
		r, ok := t.(SourceGlobable)
		if !ok {
			log.Fatalf("%T does not support WithSourceGlob", t)
		}
		r.SetSourceGlob(pattern)
	}
}

// WithContent sets literal content for the resource.
func WithContent(content string) Option {
	return func(t any) {
		r, ok := t.(Contented)
		if !ok {
			log.Fatalf("%T does not support WithContent", t)
		}
		r.SetContent(content)
	}
}

func WithLine(content string) Option {
	return func(t any) {
		r, ok := t.(LineAddable)
		if !ok {
			log.Fatalf("%T does not support WithLine", t)
		}
		r.SetAddLine(content)
	}
}

func WithoutLine(content string) Option {
	return func(t any) {
		r, ok := t.(LineRemovable)
		if !ok {
			log.Fatalf("%T does not support WithoutLine", t)
		}
		r.SetRemoveLine(content)
	}
}

// WithFileMode sets the mode applied to regular files copied from a source
// tree (distinct from the resource's own mode).
func WithFileMode(mode os.FileMode) Option {
	return func(t any) {
		r, ok := t.(FileModed)
		if !ok {
			log.Fatalf("%T does not support WithFileMode", t)
		}
		r.SetFileMode(mode)
	}
}

// WithPrune enables reconciliation of extra destination entries during a
// source copy, and recursive removal during IsAbsent().
var WithPrune = func(t any) {
	r, ok := t.(Prunable)
	if !ok {
		log.Fatalf("%T does not support WithPrune", t)
	}
	r.SetPrune()
}

func WithPruneFunc() Option { return WithPrune }

// IsAbsent marks the resource for removal.
var IsAbsent = func(t any) {
	r, ok := t.(Absentable)
	if !ok {
		log.Fatalf("%T does not support IsAbsent", t)
	}
	r.SetAbsent()
}

func IsAbsentFunc() Option { return IsAbsent }

// IsLatest marks the package resource to be updated to the latest version.
var IsLatest = func(t any) {
	r, ok := t.(Latestable)
	if !ok {
		log.Fatalf("%T does not support IsLatest", t)
	}
	r.SetLatest()
}

func IsLatestFunc() Option { return IsLatest }

// WithRestart restarts the service or timer once during this apply after
// converging to the desired running/active state.
var WithRestart = func(t any) {
	r, ok := t.(Restartable)
	if !ok {
		log.Fatalf("%T does not support WithRestart", t)
	}
	r.SetRestart()
}

func WithRestartFunc() Option { return WithRestart }

// WithReload reloads the service once during this apply when the backend
// supports a reload action. Takes precedence over WithRestart when both are set.
// Timer resources do not support WithReload.
var WithReload = func(t any) {
	r, ok := t.(Reloadable)
	if !ok {
		log.Fatalf("%T does not support WithReload", t)
	}
	r.SetReload()
}

func WithReloadFunc() Option { return WithReload }

// WithUser selects the systemd user bus (--user). Only valid on systemd.
var WithUser = func(t any) {
	r, ok := t.(UserService)
	if !ok {
		log.Fatalf("%T does not support WithUser", t)
	}
	r.SetUser()
}

func WithUserFunc() Option { return WithUser }

// WithCronUser sets the account whose crontab is managed (default "root").
func WithCronUser(user string) Option {
	return func(t any) {
		r, ok := t.(CronUserable)
		if !ok {
			log.Fatalf("%T does not support WithCronUser", t)
		}
		r.SetCronUser(user)
	}
}

// WithCommand sets the command line for a Cron resource (Puppet-inspired).
func WithCommand(cmd string) Option {
	return func(t any) {
		r, ok := t.(Commandable)
		if !ok {
			log.Fatalf("%T does not support WithCommand", t)
		}
		r.SetCommand(cmd)
	}
}

// WithMinute sets the cron minute field (default "*").
func WithMinute(v string) Option {
	return func(t any) {
		r, ok := t.(Minuteable)
		if !ok {
			log.Fatalf("%T does not support WithMinute", t)
		}
		r.SetMinute(v)
	}
}

// WithHour sets the cron hour field (default "*").
func WithHour(v string) Option {
	return func(t any) {
		r, ok := t.(Hourable)
		if !ok {
			log.Fatalf("%T does not support WithHour", t)
		}
		r.SetHour(v)
	}
}

// WithMonthday sets the cron day-of-month field (default "*").
func WithMonthday(v string) Option {
	return func(t any) {
		r, ok := t.(Monthdayable)
		if !ok {
			log.Fatalf("%T does not support WithMonthday", t)
		}
		r.SetMonthday(v)
	}
}

// WithMonth sets the cron month field (default "*").
func WithMonth(v string) Option {
	return func(t any) {
		r, ok := t.(Monthable)
		if !ok {
			log.Fatalf("%T does not support WithMonth", t)
		}
		r.SetMonth(v)
	}
}

// WithWeekday sets the cron day-of-week field (default "*").
func WithWeekday(v string) Option {
	return func(t any) {
		r, ok := t.(Weekdayable)
		if !ok {
			log.Fatalf("%T does not support WithWeekday", t)
		}
		r.SetWeekday(v)
	}
}

// WithCronEnv appends an environment assignment (KEY=VAL) above the cron line.
func WithCronEnv(kv string) Option {
	return func(t any) {
		r, ok := t.(CronEnvable)
		if !ok {
			log.Fatalf("%T does not support WithCronEnv", t)
		}
		r.AddCronEnv(kv)
	}
}

// WithSymlink makes the resource a symbolic link pointing at target.
func WithSymlink(target string) Option {
	return func(t any) {
		r, ok := t.(Linkable)
		if !ok {
			log.Fatalf("%T does not support WithSymlink", t)
		}
		r.SetSymlink(target)
	}
}

// WithHardlink makes the resource a hard link pointing at target.
func WithHardlink(target string) Option {
	return func(t any) {
		r, ok := t.(Linkable)
		if !ok {
			log.Fatalf("%T does not support WithHardlink", t)
		}
		r.SetHardlink(target)
	}
}

// WithName overrides the resource's registry name (used in its ID).
func WithName(name string) Option {
	return func(t any) {
		r, ok := t.(Named)
		if !ok {
			log.Fatalf("%T does not support WithName", t)
		}
		r.SetName(name)
	}
}

// WithDir sets the working directory for a command resource.
func WithDir(dir string) Option {
	return func(t any) {
		r, ok := t.(Dirable)
		if !ok {
			log.Fatalf("%T does not support WithDir", t)
		}
		r.SetDir(dir)
	}
}

// WithEnv merges extra environment variables into the command's environment.
func WithEnv(env map[string]string) Option {
	return func(t any) {
		r, ok := t.(Envable)
		if !ok {
			log.Fatalf("%T does not support WithEnv", t)
		}
		r.SetEnv(env)
	}
}

// Creates skips applying the command when path already exists.
func Creates(path string) Option {
	return func(t any) {
		r, ok := t.(Creatable)
		if !ok {
			log.Fatalf("%T does not support Creates", t)
		}
		r.SetCreates(path)
	}
}

// Unless skips the command when the guard probe succeeds.
func Unless(name string, args []string, opts ...GuardOption) Option {
	return func(t any) {
		r, ok := t.(Guardable)
		if !ok {
			log.Fatalf("%T does not support Unless", t)
		}
		r.SetUnless(newGuard(name, args, opts...))
	}
}

// OnlyIf runs the command only when the guard probe succeeds.
func OnlyIf(name string, args []string, opts ...GuardOption) Option {
	return func(t any) {
		r, ok := t.(Guardable)
		if !ok {
			log.Fatalf("%T does not support OnlyIf", t)
		}
		r.SetOnlyIf(newGuard(name, args, opts...))
	}
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
