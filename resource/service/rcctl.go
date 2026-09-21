package service

import "fmt"

// rcctlBackend converges services with OpenBSD rcctl(8).
type rcctlBackend struct {
	run runner
}

var _ backend = rcctlBackend{}

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
