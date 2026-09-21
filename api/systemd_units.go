package api

import (
	"slices"

	"github.com/snonux/gonf/internal/logger"
	"github.com/snonux/gonf/resource"
	"github.com/snonux/gonf/resource/options"
	svc "github.com/snonux/gonf/resource/service"
	"github.com/snonux/gonf/resource/systemd"
	"github.com/snonux/gonf/resource/timer"
)

// SystemdUnits composes the bookkeeping for installing existing systemd unit
// and drop-in files: every managed input is installed by a resource the
// caller already declared, exactly one daemon-reload watches all of them, and
// each activated unit converges after that reload — restarting only when a
// watched input changed. It removes the repeated fan-in lists and reload
// wiring without hiding any policy: privilege comes from the surrounding
// task (RequiresRoot for system units), restart policy stays in the
// ActivateTimer/ActivateService options, and reboot/drain decisions stay in
// consumer recipes.
//
// The composition reuses the low-level file, DaemonReload, Timer and Service
// resources, so recorded plans contain the same op kinds a hand-written body
// would produce:
//
//	SystemdUnits(
//	    FanIn(units, quicklogDrain),
//	    WithUserBus(),
//	    ActivateTimers(List("home-backup", "quicklog-drain")),
//	)
//
// FanIn is required: a unit set with nothing to watch can never gate its
// reload, so calling without watchable inputs is registration-time misuse.
//
// The composed daemon-reload is the per-bus singleton DaemonReload[system]
// or DaemonReload[user] (see systemd.Present), so SystemdUnits composes
// safely more than once per recipe scope: a second composition on the same
// bus, or an explicit DaemonReload there, merges into the reload already
// registered, which then watches and depends on the union of every FanIn.
// Each composition's activations still watch only their own FanIn, so a
// changed input restarts only the units that declared it. Compositions on
// different buses keep separate reloads. The merge amends the recorded
// reload op in place, which is refused (fail-fast, naming both watch lists)
// when a when-block boundary or privilege change separates the two
// declarations, or when the new declaration's inputs already depend on the
// reload (an input declared with DependsOn(an earlier composition), or that
// composition passed to FanIn), which would form a dependency cycle. A
// single composition records exactly what it did before.
func SystemdUnits(opts ...SystemdUnitsOption) Resource {
	cfg := &systemdUnitsConfig{}
	for _, o := range opts {
		o(cfg)
	}
	watch := cfg.watchedIDs()
	deps := cfg.watchedDeps()
	cfg.checkActivationNames()

	reload := cfg.composedReload(watch, deps)
	members := make([]resource.Resource, 0, 1+len(cfg.timers)+len(cfg.services))
	members = append(members, reload)
	return resource.Multi(cfg.activate(members, reload, watch, deps))
}

// checkActivationNames fails fast on an empty ActivateTimer/ActivateService
// name before anything of the composition is registered.
func (c *systemdUnitsConfig) checkActivationNames() {
	for _, a := range c.timers {
		if a.name == "" {
			logger.Fatal("SystemdUnits: ActivateTimer name must not be empty")
		}
	}
	for _, a := range c.services {
		if a.name == "" {
			logger.Fatal("SystemdUnits: ActivateService name must not be empty")
		}
	}
}

// composedReload declares the bus's daemon-reload, change-gated on and
// ordered after the FanIn inputs. systemd.Present returns the scope's
// existing reload on this bus when one is already registered, after merging
// this composition's watches and deps into it.
func (c *systemdUnitsConfig) composedReload(watch []string, deps []resource.Dependency) resource.Resource {
	reloadOpts := []options.DaemonReloadOption{
		options.WatchChanges(watch...),
		options.DependsOn(deps...),
	}
	if c.user {
		reloadOpts = append(reloadOpts, options.WithUser)
	}
	return systemd.Present(reloadOpts...)
}

// activate registers the timer and service activations (in that order),
// each after the reload and the FanIn inputs and gated on this
// composition's own inputs only, and appends them to members.
func (c *systemdUnitsConfig) activate(members []resource.Resource, reload resource.Resource, watch []string, deps []resource.Dependency) []resource.Resource {
	for _, a := range c.timers {
		timerOpts := slices.Clone(a.opts)
		if c.user {
			timerOpts = append(timerOpts, options.WithUser)
		}
		timerOpts = append(timerOpts,
			options.DependsOn(reload), options.DependsOn(deps...), options.WatchChanges(watch...))
		members = append(members, timer.Present(a.name, timerOpts...))
	}
	for _, a := range c.services {
		svcOpts := slices.Clone(a.opts)
		if c.user {
			svcOpts = append(svcOpts, options.WithUser)
		}
		svcOpts = append(svcOpts,
			options.DependsOn(reload), options.DependsOn(deps...), options.WatchChanges(watch...))
		members = append(members, svc.Present(a.name, svcOpts...))
	}
	return members
}

// SystemdUnitsOption configures a SystemdUnits composition.
type SystemdUnitsOption func(*systemdUnitsConfig)

type systemdUnitsConfig struct {
	user     bool
	inputs   []Resource
	timers   []unitsTimerActivation
	services []unitsServiceActivation
}

type unitsTimerActivation struct {
	name string
	opts []options.TimerOption
}

type unitsServiceActivation struct {
	name string
	opts []options.ServiceOption
}

// watchedIDs flattens the FanIn inputs into a deduplicated, first-seen
// ordered watch list; Multi inputs (e.g. SyncDir results) expand to their
// member ids. Registration-time misuse — no FanIn, or inputs that expand to
// no resource ids — aborts the recipe before anything is registered.
func (c *systemdUnitsConfig) watchedIDs() []string {
	seen := make(map[string]struct{}, len(c.inputs))
	var ids []string
	for _, in := range c.inputs {
		for _, id := range in.Dependencies() {
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		logger.Fatal("SystemdUnits: no watchable managed inputs; pass FanIn(...) with resources that expand to registered resource ids")
	}
	return ids
}

// watchedDeps returns the FanIn inputs as dependency values, so the watched
// resources apply before the composed reload and activations; duplicated ids
// across inputs collapse when the drafts sort their dependency ids.
func (c *systemdUnitsConfig) watchedDeps() []resource.Dependency {
	deps := make([]resource.Dependency, 0, len(c.inputs))
	for _, in := range c.inputs {
		deps = append(deps, in)
	}
	return deps
}

// FanIn declares the managed inputs of a SystemdUnits set: unit files,
// drop-ins, and the scripts or defaults those units run. Every input fans
// into the bus's single daemon-reload (shared with any other composition on
// that bus in the same recipe scope) and into every activation's change
// gate of this composition, so one changed file reloads systemd once and
// restarts only the units whose activation requested a restart policy.
// Multi resources (for example SyncDir results) expand to their members.
func FanIn(inputs ...Resource) SystemdUnitsOption {
	return func(c *systemdUnitsConfig) {
		c.inputs = append(c.inputs, inputs...)
	}
}

// WithUserBus routes the composed daemon-reload and unit activations through
// the systemd user bus (~/.config/systemd/user) instead of the system bus.
// Pass it once per composition.
func WithUserBus() SystemdUnitsOption {
	return func(c *systemdUnitsConfig) {
		c.user = true
	}
}

// ActivateTimer activates one timer unit after the composed daemon-reload.
// The unit converges to enabled and started on every apply; WithRestart adds
// a restart that fires only when a FanIn input changed. Names without a
// ".timer" suffix get one appended.
func ActivateTimer(name string, opts ...options.TimerOption) SystemdUnitsOption {
	return func(c *systemdUnitsConfig) {
		c.timers = append(c.timers, unitsTimerActivation{name: name, opts: opts})
	}
}

// ActivateTimers is ActivateTimer for a list of names sharing one option set.
func ActivateTimers(names []string, opts ...options.TimerOption) SystemdUnitsOption {
	return func(c *systemdUnitsConfig) {
		for _, name := range names {
			c.timers = append(c.timers, unitsTimerActivation{name: name, opts: opts})
		}
	}
}

// ActivateService activates one service unit after the composed daemon-reload.
// The unit converges to enabled and active on every apply; WithRestart or
// WithReload fire only when a FanIn input changed.
func ActivateService(name string, opts ...options.ServiceOption) SystemdUnitsOption {
	return func(c *systemdUnitsConfig) {
		c.services = append(c.services, unitsServiceActivation{name: name, opts: opts})
	}
}

// ActivateServices is ActivateService for a list of names sharing one option
// set.
func ActivateServices(names []string, opts ...options.ServiceOption) SystemdUnitsOption {
	return func(c *systemdUnitsConfig) {
		for _, name := range names {
			c.services = append(c.services, unitsServiceActivation{name: name, opts: opts})
		}
	}
}
