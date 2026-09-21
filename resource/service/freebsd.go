package service

import "fmt"

// freebsdBackend converges services with FreeBSD service(8), whose
// enable/disable verbs edit rc.conf itself.
type freebsdBackend struct {
	run runner
}

var _ backend = freebsdBackend{}

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
