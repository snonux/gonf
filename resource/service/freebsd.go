package service

import (
	"fmt"
	"strings"
)

// freebsdBackend converges services with FreeBSD service(8), whose
// enable/disable verbs edit rc.conf itself.
type freebsdBackend struct {
	run runner
}

var (
	_ backend = freebsdBackend{}
	_ flagger = freebsdBackend{}
)

// userSupport refuses WithUser: service(8) has no per-user services.
func (freebsdBackend) userSupport() error { return errUserNeedsSystemd }

// running asks service NAME status, which exits 0 while the daemon runs.
func (b freebsdBackend) running(u unit) (bool, error) {
	return probeExitZero(b.run, "service "+u.name+" status", "service", u.name, "status")
}

// enabled asks service NAME enabled, which exits 0 when enabled in rc.conf.
func (b freebsdBackend) enabled(u unit) (bool, error) {
	return probeExitZero(b.run, "service "+u.name+" enabled", "service", u.name, "enabled")
}

func (b freebsdBackend) do(u unit, v verb) error {
	return runChecked(b.run, "service", u.name, string(v))
}

func (freebsdBackend) describe(u unit, v verb) (would, did string) {
	args := []string{u.name, string(v)}
	return fmt.Sprintf("run service %v", args), fmt.Sprintf("service %v", args)
}

// flagsMatch asks sysrc -n -i NAME_flags, which prints the rc.conf value
// (/etc/defaults/rc.conf's included), or nothing for an unset variable, and
// compares it with want. An unset variable therefore matches empty flags:
// rc.subr starts the daemon without extra flags either way.
func (b freebsdBackend) flagsMatch(u unit, want string) (bool, error) {
	name, err := flagsVar(u.name)
	if err != nil {
		return false, err
	}
	stdout, stderr, code, err := b.run("sysrc", "-n", "-i", name)
	if err != nil {
		return false, fmt.Errorf("sysrc -n -i %s: %w", name, err)
	}
	if code != 0 {
		return false, fmt.Errorf("sysrc -n -i %s failed (exit %d): %s%s", name, code, stdout, stderr)
	}
	return strings.TrimSuffix(stdout, "\n") == want, nil
}

// setFlags runs sysrc NAME_flags=FLAGS as one argv (no shell), which
// rewrites the assignment in /etc/rc.conf; sysrc quotes the value itself.
func (b freebsdBackend) setFlags(u unit, flags string) error {
	name, err := flagsVar(u.name)
	if err != nil {
		return err
	}
	return runChecked(b.run, "sysrc", name+"="+flags)
}

func (freebsdBackend) describeFlags(u unit, flags string) (would, did string) {
	args := []string{u.name + "_flags=" + flags}
	return fmt.Sprintf("run sysrc %v", args), fmt.Sprintf("sysrc %v", args)
}
