package service

import (
	"errors"

	"github.com/snonux/gonf/resource/systemd"
)

// systemdBackend converges services via systemctl, through the shared
// mechanics in resource/systemd (whose runner testseam.FakeServiceRunner also
// fakes).
// Unlike Timer, Service performs no unit-name validation here — the name is
// passed to systemctl as given. That drift is deliberate for now: validation
// stays with the callers that had it.
type systemdBackend struct{}

var _ backend = systemdBackend{}

// errUserNeedsSystemd is the WithUser refusal reason of every backend
// without a per-user manager: systemd is the only supported manager with a
// per-user instance (systemctl --user). The wording is user-visible and
// pinned by tests.
var errUserNeedsSystemd = errors.New("WithUser is only supported on systemd")

// userSupport returns nil: systemd has a per-user instance.
func (systemdBackend) userSupport() error { return nil }

func (systemdBackend) running(u unit) (bool, error) { return systemd.IsActive(u.name, u.user) }

func (systemdBackend) enabled(u unit) (bool, error) { return systemd.IsEnabled(u.name, u.user) }

func (systemdBackend) do(u unit, v verb) error { return command(u, v).Do() }

func (systemdBackend) describe(u unit, v verb) (would, did string) { return command(u, v).Describe() }

// command returns the systemctl invocation performing v on u (with --user
// for a per-user unit). Timer builds its actions from the same
// systemd.Command, so both render and run systemctl identically.
func command(u unit, v verb) systemd.Command {
	return systemd.Command(systemd.Args(u.user, string(v), u.name))
}
