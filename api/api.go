package api

import (
	"slices"

	"github.com/snonux/gonf/resource"
	"github.com/snonux/gonf/resource/cmd"
	"github.com/snonux/gonf/resource/cron"
	"github.com/snonux/gonf/resource/dir"
	"github.com/snonux/gonf/resource/file"
	"github.com/snonux/gonf/resource/link"
	"github.com/snonux/gonf/resource/options"
	"github.com/snonux/gonf/resource/pkg"
	svc "github.com/snonux/gonf/resource/service"
	"github.com/snonux/gonf/resource/systemd"
	"github.com/snonux/gonf/resource/systemdtimer"
	"github.com/snonux/gonf/resource/timer"
)

// Path constraint for resources that can be defined as a single item or a list.
type Path interface {
	string | []string
}

// File creates one or more file resources.
func File[T Path](path T, opts ...options.FileOption) Resource {
	switch v := any(path).(type) {
	case string:
		return file.Present(v, opts...)
	case []string:
		return Files(v, opts...)
	default:
		panic("File: path must be string or []string")
	}
}

// Files is File for a list of paths: it returns one Multi resource
// containing a file resource per path.
func Files(paths []string, opts ...options.FileOption) Resource {
	var resources []resource.Resource
	for _, path := range paths {
		resources = append(resources, file.Present(path, opts...))
	}
	return resource.Multi(resources)
}

// NoFile creates one or more file resources that are ensured to be absent.
func NoFile[T Path](path T, opts ...options.FileOption) Resource {
	return File(path, append(slices.Clone(opts), options.IsAbsent)...)
}

// Dir creates one or more directory resources.
func Dir[T Path](path T, opts ...options.DirOption) Resource {
	switch v := any(path).(type) {
	case string:
		return dir.Present(v, opts...)
	case []string:
		return Dirs(v, opts...)
	default:
		panic("Dir: path must be string or []string")
	}
}

// Dirs is Dir for a list of paths: it returns one Multi resource
// containing a directory resource per path.
func Dirs(paths []string, opts ...options.DirOption) Resource {
	var resources []resource.Resource
	for _, path := range paths {
		resources = append(resources, dir.Present(path, opts...))
	}
	return resource.Multi(resources)
}

// NoDir creates one or more directory resources that are ensured to be absent.
func NoDir[T Path](path T, opts ...options.DirOption) Resource {
	return Dir(path, append(slices.Clone(opts), options.IsAbsent)...)
}

// Link creates one or more link resources (symbolic or hard).
func Link[T Path](path T, opts ...options.LinkOption) Resource {
	switch v := any(path).(type) {
	case string:
		return link.Present(v, opts...)
	case []string:
		return Links(v, opts...)
	default:
		panic("Link: path must be string or []string")
	}
}

// Links is Link for a list of paths: it returns one Multi resource
// containing a link resource per path.
func Links(paths []string, opts ...options.LinkOption) Resource {
	var resources []resource.Resource
	for _, path := range paths {
		resources = append(resources, link.Present(path, opts...))
	}
	return resource.Multi(resources)
}

// NoLink creates one or more link resources that are ensured to be absent.
func NoLink[T Path](path T, opts ...options.LinkOption) Resource {
	return Link(path, append(slices.Clone(opts), options.IsAbsent)...)
}

// List is the DSL alias for a []string: prefer List("a", "b") over
// []string{"a", "b"} in recipes (multi-path resources, Command args,
// WhenHostname hosts, EachKV pairs, and similar).
func List(paths ...string) []string {
	return paths
}

// Package creates one or more package resources.
func Package[T Path](name T, opts ...options.PackageOption) Resource {
	switch v := any(name).(type) {
	case string:
		return pkg.Present(v, opts...)
	case []string:
		return Packages(v, opts...)
	default:
		panic("Package: name must be string or []string")
	}
}

// Packages is Package for a list of names: it returns one Multi resource
// containing a package resource per name.
func Packages(names []string, opts ...options.PackageOption) Resource {
	var resources []resource.Resource
	for _, name := range names {
		resources = append(resources, pkg.Present(name, opts...))
	}
	return resource.Multi(resources)
}

// NoPackage creates one or more package resources that are ensured to be absent.
func NoPackage[T Path](name T, opts ...options.PackageOption) Resource {
	return Package(name, append(slices.Clone(opts), options.IsAbsent)...)
}

// Service ensures one or more OS services are running and enabled at boot.
// The backend is selected automatically: systemd on Linux, rcctl on OpenBSD,
// service(8) on FreeBSD and NetBSD.
func Service[T Path](name T, opts ...options.ServiceOption) Resource {
	switch v := any(name).(type) {
	case string:
		return svc.Present(v, opts...)
	case []string:
		return Services(v, opts...)
	default:
		panic("Service: name must be string or []string")
	}
}

// Services is Service for a list of names: it returns one Multi resource
// containing a service resource per name.
func Services(names []string, opts ...options.ServiceOption) Resource {
	var resources []resource.Resource
	for _, name := range names {
		resources = append(resources, svc.Present(name, opts...))
	}
	return resource.Multi(resources)
}

// NoService ensures one or more OS services are stopped and disabled.
func NoService[T Path](name T, opts ...options.ServiceOption) Resource {
	return Service(name, append(slices.Clone(opts), options.IsAbsent)...)
}

// Cron ensures a named crontab entry for a user (default root).
// Schedule fields default to "*". Requires WithCommand unless absent.
// Inspired by Puppet's cron type (command, user, minute/hour/monthday/month/weekday, env).
func Cron(name string, opts ...options.CronOption) Resource {
	return cron.Present(name, opts...)
}

// NoCron removes a named crontab entry (use WithCronUser for non-root).
func NoCron(name string, opts ...options.CronOption) Resource {
	return cron.Absent(name, opts...)
}

// Timer ensures one or more systemd .timer units are active and enabled.
// Linux/systemd only. Names without a ".timer" suffix get one appended.
// Use WithUser for the systemd user bus. WithEnableOnly skips start/stop.
func Timer[T Path](name T, opts ...options.TimerOption) Resource {
	switch v := any(name).(type) {
	case string:
		return timer.Present(v, opts...)
	case []string:
		return Timers(v, opts...)
	default:
		panic("Timer: name must be string or []string")
	}
}

// Timers is Timer for a list of names: it returns one Multi resource
// containing a timer resource per name.
func Timers(names []string, opts ...options.TimerOption) Resource {
	var resources []resource.Resource
	for _, name := range names {
		resources = append(resources, timer.Present(name, opts...))
	}
	return resource.Multi(resources)
}

// NoTimer ensures one or more systemd .timer units are stopped and disabled.
func NoTimer[T Path](name T, opts ...options.TimerOption) Resource {
	return Timer(name, append(slices.Clone(opts), options.IsAbsent)...)
}

// DaemonReload runs systemctl daemon-reload (or --user). Prefer OnChange(unitFiles)
// to reload only when unit files changed; the legacy DependsOn + IfChanged form
// remains supported.
func DaemonReload(opts ...options.DaemonReloadOption) Resource {
	return systemd.Present(opts...)
}

// SystemdTimer installs a .timer + companion oneshot .service under
// /etc/systemd/system (or ~/.config/systemd/user with WithUser), daemon-reloads
// when the units change, and enables/starts the timer. Requires WithCommand and
// WithOnCalendar unless absent.
func SystemdTimer(name string, opts ...options.SystemdTimerOption) Resource {
	return systemdtimer.Present(name, opts...)
}

// NoSystemdTimer stops/disables a timer and removes its unit files.
func NoSystemdTimer(name string, opts ...options.SystemdTimerOption) Resource {
	return systemdtimer.Absent(name, opts...)
}

// Command registers a command resource that runs name with args on Apply.
// Use options.Unless, options.OnlyIf, or options.Creates for idempotency.
func Command(name string, args []string, opts ...options.CommandOption) Resource {
	return cmd.Present(name, args, opts...)
}
