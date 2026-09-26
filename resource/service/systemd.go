package service

import (
	"errors"
	"slices"
	"strings"

	"github.com/snonux/gonf/resource/systemd"
)

// systemdBackend converges services via systemctl, through client, the
// systemd runner override selectBackend wired it with (task 4e2: the zero
// Client reaches the real runner). Unlike Timer, Service performs no
// unit-name validation here — the name is passed to systemctl as given.
// That drift is deliberate for now: validation stays with the callers that
// had it.
type systemdBackend struct {
	client systemd.Client
}

var _ backend = systemdBackend{}

// errUserNeedsSystemd is the WithUser refusal reason of every backend
// without a per-user manager: systemd is the only supported manager with a
// per-user instance (systemctl --user). The wording is user-visible and
// pinned by tests.
var errUserNeedsSystemd = errors.New("WithUser is only supported on systemd")

// userSupport returns nil: systemd has a per-user instance.
func (systemdBackend) userSupport() error { return nil }

func (b systemdBackend) running(u unit) (bool, error) { return b.client.IsActive(u.name, u.user) }

var _ enablementProber = systemdBackend{}

func (b systemdBackend) enabled(u unit) (bool, error) { return b.client.IsEnabled(u.name, u.user) }

// enablement probes systemctl is-enabled once and maps the printed state to
// the verb that removes it (systemd.Enablement.DisableOp): none for a
// static, indirect, generated, alias or transient unit, which disable
// cannot change, and disable --runtime for enabled-runtime.
func (b systemdBackend) enablement(u unit) (bool, verb, error) {
	e, err := b.client.Enablement(u.name, u.user)
	if err != nil {
		return false, "", err
	}
	switch op := e.DisableOp(); {
	case op == nil:
		return e.Enabled, "", nil
	case slices.Equal(op, []string{"disable", "--runtime"}):
		return e.Enabled, verbDisableRuntime, nil
	default:
		return e.Enabled, verbDisable, nil
	}
}

func (b systemdBackend) do(u unit, v verb) error { return b.command(u, v).Do() }

func (b systemdBackend) describe(u unit, v verb) (would, did string) {
	return b.command(u, v).Describe()
}

// command returns the systemctl invocation performing v on u (with --user
// for a per-user unit), through b's client. A verb carrying a flag
// (verbDisableRuntime, "disable --runtime") is split into its words. Timer builds its actions from
// the same systemd.Client.Command, so both render and run systemctl
// identically.
func (b systemdBackend) command(u unit, v verb) systemd.Command {
	op := append(strings.Fields(string(v)), u.name)
	return b.client.Command(systemd.Args(u.user, op...))
}
