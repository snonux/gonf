package service

import (
	"fmt"
	"os"
	"path/filepath"
)

const netbsdService = "/usr/sbin/service"

// netbsdRcConfD is the production rc.conf.d override directory the NetBSD
// backend writes enable/disable overrides into. Tests build a netbsdBackend
// with a temporary rcConfD instead of touching /etc.
const netbsdRcConfD = "/etc/rc.conf.d"

// netbsdBackend converges services with NetBSD service(8). NetBSD's
// service(8) has no enable/disable verbs, so those write an rc.conf.d
// override (NAME=YES|NO) instead of running a command. WithFlags edits
// NAME_flags in rcConf (netbsd_flags.go).
type netbsdBackend struct {
	run            runner
	rcConfD        string // override directory, netbsdRcConfD in production
	rcConf         string // netbsdRcConf in production
	rcConfDefaults string // netbsdRcConfDefaults in production
}

var _ backend = netbsdBackend{}

// userSupport refuses WithUser: service(8) has no per-user services.
func (netbsdBackend) userSupport() error { return errUserNeedsSystemd }

// running asks service NAME onestatus, which exits 0 while the daemon runs.
// NetBSD rc.subr refuses every directive but rcvar with exit 1 ("$NAME is
// not enabled") while the service's rcvar is not YES, status included, so
// plain status would report a running but disabled daemon as stopped. The
// one* form skips that check.
func (b netbsdBackend) running(u unit) (bool, error) {
	return probeExitZero(b.run, "service "+u.name+" onestatus", netbsdService, u.name, "onestatus")
}

// enabled asks service -e NAME, which exits 0 when the service is enabled.
func (b netbsdBackend) enabled(u unit) (bool, error) {
	return probeExitZero(b.run, "service -e "+u.name, netbsdService, "-e", u.name)
}

func (b netbsdBackend) do(u unit, v verb) error {
	switch v {
	case verbEnable:
		return b.setEnabled(u.name, true)
	case verbDisable:
		return b.setEnabled(u.name, false)
	default:
		return b.svcRun(u.name, rcAction(v))
	}
}

// rcAction is the rc.d directive performing v: its one* form, which rc.subr
// runs whatever the service's rcvar says. Plain stop (or start, restart,
// reload) exits 1 for a service that is not enabled, so an absent service
// started by hand could never be stopped, and ordering does not guarantee
// the rcvar is already YES when a present service starts.
func rcAction(v verb) string { return "one" + string(v) }

// describe names rc.conf.d edits as "enable NAME"/"disable NAME" and
// service(8) calls as "service NAME oneVERB", in both dry-run and apply logs.
func (netbsdBackend) describe(u unit, v verb) (would, did string) {
	desc := "service " + u.name + " " + rcAction(v)
	if v == verbEnable || v == verbDisable {
		desc = string(v) + " " + u.name
	}
	return desc, desc
}

func (b netbsdBackend) svcRun(name, action string) error {
	stdout, stderr, code, err := b.run(netbsdService, name, action)
	if err != nil {
		return fmt.Errorf("service %s %s: %w", name, action, err)
	}
	if code != 0 {
		return fmt.Errorf("service %s %s failed (exit %d): %s%s", name, action, code, stdout, stderr)
	}
	return nil
}

// setEnabled writes rcConfD/NAME with NAME=YES|NO (overrides rc.conf).
func (b netbsdBackend) setEnabled(name string, enabled bool) error {
	if err := os.MkdirAll(b.rcConfD, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", b.rcConfD, err)
	}
	val := "NO"
	if enabled {
		val = "YES"
	}
	content := name + "=" + val + "\n"
	path := filepath.Join(b.rcConfD, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
