package options

// Compact spellings for common resource settings: WithSchedule sets all
// five cron schedule fields at once, and WithFlags sets a BSD service's
// startup flags. They live apart from option.go's long lists so the
// capability interfaces they need sit next to the options that use them.

import (
	"errors"
	"strings"
)

// Capability interfaces of the options in this file.
type (
	// Scheduleable takes a whole five-field cron schedule (WithSchedule).
	// The resource validates it and reports misuse itself, since it owns
	// the cron field syntax.
	Scheduleable interface{ SetSchedule(string) }
	// Flaggable takes a service's startup flags (WithFlags).
	Flaggable interface{ SetFlags(string) }
)

// WithSchedule sets a cron job's five schedule fields from one crontab-style
// string, minute hour monthday month weekday: WithSchedule("10 6 * * *") is
// WithMinute("10"), WithHour("6") and the other three at "*". Fields are
// separated by spaces or tabs. Anything but five portable fields (including
// @ directives such as @reboot, which Cron does not support) is
// declaration-time misuse. A per-field option given after it overrides its
// one field.
func WithSchedule(schedule string) cronOption {
	return cronOption(func(target any) {
		requires(target, "WithSchedule", func(r Scheduleable) { r.SetSchedule(schedule) })
	})
}

// WithFlags sets a service's startup flags on the BSD backends: OpenBSD
// `rcctl set NAME flags ...` (stored in /etc/rc.conf.local), FreeBSD
// `sysrc NAME_flags=...` and NetBSD `NAME_flags=...` in /etc/rc.conf. An
// empty string manages the flags to empty, which on OpenBSD is the plain
// `NAME_flags=` line rcctl writes when it enables a base daemon. A flags
// change counts as a change of the service: with WithRestart (or
// WithReload) it restarts a running service even when its OnChange gate
// would hold. systemd has no flags variable, so a service with WithFlags
// fails on a systemd host at apply time, before anything changes. A flags
// value with a line break is declaration-time misuse.
func WithFlags(flags string) serviceOption {
	return serviceOption(func(target any) {
		requires(target, "WithFlags", func(r Flaggable) {
			if strings.ContainsAny(flags, "\n\r") {
				misuse(target, errors.New("WithFlags must be a single line"))
				return
			}
			r.SetFlags(flags)
		})
	})
}
