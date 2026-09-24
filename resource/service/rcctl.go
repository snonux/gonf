package service

import (
	"fmt"
	"strings"
)

// rcctlBackend converges services with OpenBSD rcctl(8).
type rcctlBackend struct {
	run runner
}

var (
	_ backend = rcctlBackend{}
	_ flagger = rcctlBackend{}
)

// userSupport refuses WithUser: rcctl has no per-user daemons.
func (rcctlBackend) userSupport() error { return errUserNeedsSystemd }

// running asks rcctl check, which exits 0 while the daemon runs.
func (b rcctlBackend) running(u unit) (bool, error) {
	return probeExitZero(b.run, "rcctl check "+u.name, "rcctl", "check", u.name)
}

// enabled asks rcctl get NAME status. Package daemons often print nothing;
// exit 0 means enabled (status on).
func (b rcctlBackend) enabled(u unit) (bool, error) {
	return probeExitZero(b.run, "rcctl get "+u.name+" status", "rcctl", "get", u.name, "status")
}

func (b rcctlBackend) do(u unit, v verb) error {
	return runChecked(b.run, "rcctl", string(v), u.name)
}

func (rcctlBackend) describe(u unit, v verb) (would, did string) {
	args := []string{string(v), u.name}
	return fmt.Sprintf("run rcctl %v", args), fmt.Sprintf("rcctl %v", args)
}

// flagsMatch asks rcctl get NAME flags, which prints the flags rc(8) starts
// the daemon with (the NAME_flags value rcctl stores in /etc/rc.conf.local,
// else the default), and compares it with want. flagsUpdate only asks for
// an enabled daemon.
func (b rcctlBackend) flagsMatch(u unit, want string) (bool, error) {
	stdout, stderr, code, err := b.run("rcctl", "get", u.name, "flags")
	if err != nil {
		return false, fmt.Errorf("rcctl get %s flags: %w", u.name, err)
	}
	if code != 0 {
		return false, fmt.Errorf("rcctl get %s flags failed (exit %d): %s%s", u.name, code, stdout, stderr)
	}
	return strings.TrimSuffix(stdout, "\n") == want, nil
}

// setFlags runs rcctl set NAME flags FLAGS, passing the flags as one
// argument (rcctl joins its arguments into the stored value, so one or
// several make no difference). Empty flags pass no argument: rcctl then
// writes the plain NAME_flags= line for a base daemon that is off by
// default (httpd, relayd, ...), exactly what `rcctl enable` writes for it,
// and drops the line otherwise. rcctl refuses set for a disabled daemon,
// which sequence rules out by running this after the enable.
func (b rcctlBackend) setFlags(u unit, flags string) error {
	return runChecked(b.run, "rcctl", rcctlSetFlagsArgs(u, flags)...)
}

func (rcctlBackend) describeFlags(u unit, flags string) (would, did string) {
	args := rcctlSetFlagsArgs(u, flags)
	return fmt.Sprintf("run rcctl %v", args), fmt.Sprintf("rcctl %v", args)
}

// rcctlSetFlagsArgs is the rcctl argv that stores flags for u.
func rcctlSetFlagsArgs(u unit, flags string) []string {
	args := []string{"set", u.name, "flags"}
	if flags != "" {
		args = append(args, flags)
	}
	return args
}
