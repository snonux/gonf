package systemd

import (
	"path/filepath"
	"slices"
	"strings"

	"github.com/snonux/gonf/internal/logger"
	"github.com/snonux/gonf/resource"
)

// JoinRegisteredReload lets a resource that reloads the manager itself, as
// part of one composite op, share the bus's registered daemon-reload
// instead of adding a second reload to the apply. SystemdTimer is the
// caller: its op writes its unit files, reloads privately when they changed
// and only then converges the timer, so the reload cannot move out of the
// op. Instead the registered reload on the same bus (DaemonReload[user]
// for user, DaemonReload[system] otherwise; typically from SystemdUnits) is
// ordered after the joiner by one extra dependency edge on joinerID. The
// joiner then applies first, and its private reload also loads every
// input the registered reload watches that was written before it; the
// registered reload, whose gate only fires on a change noted after the
// bus's last reload (changedSinceLastReload), is then held unless another
// input changed later. So the bus reloads at most once for the inputs
// written before the joiner, and every unit is still loaded before it is
// activated: the registered reload's activations run after it, and the
// joiner's own enable/start/restart after its own reload. That joiner
// activation is no longer after the registered reload, though: when only
// the registered reload's inputs changed, the joiner restarts before they
// are reloaded. Its own units did not change then, which is why related
// (below) must not name one of those inputs. Only the dependency list of
// the recorded reload changes, so the plan schema does not.
//
// related are the units the joiner's own units are or reference (for
// SystemdTimer: its own <base>.service and <base>.timer, plus its After=
// and Wants= entries; an entry may list several space-separated units,
// which are checked one by one). Starting the joiner before the registered
// reload could otherwise start such a unit from a stale definition: e.g. a
// Persistent timer that fires on start with a stale drop-in on its own
// service, or whose service wants a composition service whose changed unit
// file is only loaded by the later reload. The
// check sees the reload's inputs at join time only, so a successful join is
// remembered (DaemonReloadResource.joiners) and a later same-bus declaration
// whose inputs may define one of the joiner's related units is refused when
// it merges (see mergeInto): the joiner's edge cannot be removed again.
//
// It returns false, and leaves everything as it was (the joiner then keeps
// its private reload after the registered one, two reloads at worst, as
// before), when:
//   - no daemon-reload is registered on that bus in this recipe scope yet
//     (a SystemdTimer alone, one on the other bus, or one declared before
//     the reload: it then applies before the reload's inputs are written,
//     so its reload could not cover them anyway);
//   - a related unit may be one of the reload's inputs (mayManageUnit);
//   - resource.AmendRegistered refuses the edge: the joiner already
//     depends on the reload (DependsOn a SystemdUnits composition), which
//     would close a cycle, or a when-block boundary or privilege change
//     separates the recorded reload from the joiner.
//
// A registered reload that is not change-gated always reloads, so joining
// it only fixes the order; the joiner's own reload still runs on change.
func JoinRegisteredReload(user bool, joinerID string, related ...string) bool {
	id := busReloadID(user)
	r, d, ok := registeredReload(id)
	if !ok {
		return false
	}
	if slices.Contains(d.DependsOn.IDs, joinerID) {
		return true
	}
	if unit, ok := d.relatedInput(related); ok {
		logger.Debug("%s: %s keeps its own daemon-reload: it references %s, which may be an input of this bus's registered reload", id, joinerID, unit)
		return false
	}
	m := *d
	m.DependsOn.IDs = append(slices.Clone(d.DependsOn.IDs), joinerID)
	m.joiners = append(slices.Clone(d.joiners), reloadJoiner{id: joinerID, related: slices.Clone(related)})
	if err := resource.AmendRegistered(m.planDraft(r.ID()), joinerID); err != nil {
		logger.Debug("%s: %s keeps its own daemon-reload, not ordered before this bus's registered one: %v", id, joinerID, err)
		return false
	}
	*d = m
	logger.Debug("%s: ordered after %s, which shares this bus's reload", id, joinerID)
	return true
}

// joinerRelatedInput returns the first joiner of d, and its related unit,
// that one of d's inputs may define (relatedInput), and whether there is
// one. mergeInto calls it on the merged reload, whose inputs include the
// new declaration's.
func (d *DaemonReloadResource) joinerRelatedInput() (reloadJoiner, string, bool) {
	for _, j := range d.joiners {
		if unit, ok := d.relatedInput(j.related); ok {
			return j, unit, true
		}
	}
	return reloadJoiner{}, "", false
}

// relatedInput returns the first unit named by entries that one of d's
// inputs (its watched ids, and the registered files under a watched
// directory) may define, and whether there is one. Each entry is split on
// whitespace first (strings.Fields), the way systemd reads the rendered
// After=/Wants= line: WithWants("network-online.target a.service") names
// two units, and each is checked on its own (cb2).
func (d *DaemonReloadResource) relatedInput(entries []string) (string, bool) {
	if len(entries) == 0 {
		return "", false
	}
	inputs := slices.Concat(d.Watch, resource.RegisteredWatchTargets(d.Watch...))
	for _, entry := range entries {
		for _, unit := range strings.Fields(entry) {
			if slices.ContainsFunc(inputs, func(in string) bool { return mayManageUnit(in, unit) }) {
				return unit, true
			}
		}
	}
	return "", false
}

// mayManageUnit reports whether the resource id may install the unit file
// or a drop-in of unit. Only a File[path] can be judged by name: its base
// name is the unit (or its template, a@.service for a@x.service), or its
// parent directory is one of the unit's drop-in directories: unit.d (or the
// template's), a dash-prefix directory, or the bare type-wide directory
// (dropinDirs). The two directories named by dropinDirs only match when the
// drop-in directory itself sits directly under a real systemd unit search
// directory (isUnitSearchDir): systemd only ever scans them there
// (systemd.unit(5)), so e.g. /srv/data/service.d/x.conf must not be treated
// as a drop-in of every .service unit just because its parent is named
// "service.d" (zc2). unit.d itself is left unscoped: it names the unit, so
// a same-named directory elsewhere is not a plausible false positive the
// way the type-wide and dash-prefix directories are. Any other kind,
// including a Directory (a SyncDir's files are not registered one by one),
// may hold any unit and counts as managing it: the check must never let a
// join start a unit from a stale definition.
func mayManageUnit(id, unit string) bool {
	kind, path, ok := strings.Cut(strings.TrimSuffix(id, "]"), "[")
	if !ok || kind != "File" {
		return true
	}
	names := []string{unit}
	if tmpl, ok := templateName(unit); ok {
		names = append(names, tmpl)
	}
	base, dir := filepath.Base(path), filepath.Dir(path)
	parent := filepath.Base(dir)
	for _, n := range names {
		if base == n || parent == n+".d" {
			return true
		}
		if slices.Contains(dropinDirs(n), parent) && isUnitSearchDir(filepath.Dir(dir)) {
			return true
		}
	}
	return false
}

// unitSearchDirs are the standard systemd unit load directories under which
// systemd also scans a unit's dash-prefix and bare type-wide drop-in
// directories (systemd.unit(5), "Unit Load Path"). Gonf's own SystemdTimer
// only ever writes units to /etc/systemd/system or ~/.config/systemd/user
// (resource/systemdtimer.unitDir), but a File input naming any other
// standard search directory is just as real a drop-in location, so
// isUnitSearchDir recognizes the documented set, not just gonf's own two.
//
// The dbus-transient directories (*.control, transient, generator[.early|
// .late]) are deliberately left out: they hold configuration systemd itself
// writes at runtime (a "systemctl set-property", a generator script), never
// a plausible target for a hand-written gonf File drop-in, so treating them
// as unit search directories would only widen mayManageUnit's false-positive
// surface without guarding a real gonf use case (dd2).
var unitSearchDirs = []string{
	"/etc/systemd/system",
	"/run/systemd/system",
	"/usr/lib/systemd/system",
	"/usr/local/lib/systemd/system",
	"/lib/systemd/system", // pre-merged-/usr layout; usually a symlink to /usr/lib/systemd/system
	"/etc/systemd/user",
	"/etc/xdg/systemd/user", // $XDG_CONFIG_DIRS/systemd/user default (dd2)
	"/run/systemd/user",
	"/usr/lib/systemd/user",
	"/usr/local/lib/systemd/user",
	"/usr/share/systemd/user",
	"/usr/local/share/systemd/user",
}

// isUnitSearchDir reports whether dir is one of the standard systemd unit
// load directories (unitSearchDirs), or one of the two per-user directories
// whose default location is under the user's home and so is matched by
// suffix instead (the home directory varies per host and user):
// ~/.config/systemd/user ($XDG_CONFIG_HOME default) and
// ~/.local/share/systemd/user ($XDG_DATA_HOME default, dd2).
//
// This only matches the documented XDG *defaults*, not a host's actual
// $XDG_CONFIG_HOME/$XDG_DATA_HOME: mayManageUnit runs while a recipe
// declares its resources, on the controller process, which has no per-
// destination-host environment to consult (a recipe is declared once and
// applied to any number of destination hosts, each with its own). Reading
// the controller's own environment variables here would silently check the
// wrong host's settings, so the suffix match (widened to cover the
// documented default set, not the controller's environment) stays the
// right tool for a controller-side, host-agnostic check (dd2).
func isUnitSearchDir(dir string) bool {
	if slices.Contains(unitSearchDirs, dir) {
		return true
	}
	return strings.HasSuffix(dir, "/.config/systemd/user") ||
		strings.HasSuffix(dir, "/.local/share/systemd/user")
}

// dropinDirs returns the drop-in directory names systemd additionally
// searches for unit, beyond unit.d itself (systemd.unit(5), "Configuration
// Directories and Precedence"):
//   - one per dash-prefix of the unit's name, progressively truncated after
//     each remaining '-': for foo-bar-baz.service that is foo-bar-.service.d
//     and foo-.service.d, so a set of related units sharing a name prefix
//     can share drop-ins;
//   - the bare <type>.d directory (e.g. service.d), which applies to every
//     unit of that type, not just ones with a dash-prefix.
//
// It returns only the directory names, unqualified by location: systemd
// reads them wherever it finds them within its unit search path, so
// mayManageUnit (their only caller) additionally requires the directory's
// own parent to be one (isUnitSearchDir) before treating a same-named
// directory elsewhere as a match.
func dropinDirs(unit string) []string {
	dot := strings.LastIndexByte(unit, '.')
	if dot < 0 {
		return nil
	}
	typ, name := unit[dot+1:], unit[:dot]
	var dirs []string
	for i := strings.LastIndexByte(name, '-'); i >= 0; i = strings.LastIndexByte(name, '-') {
		name = name[:i]
		dirs = append(dirs, name+"-."+typ+".d")
	}
	return append(dirs, typ+".d")
}

// templateName returns the template unit of an instance name
// (a@x.service -> a@.service), and false for a unit that is no instance.
func templateName(unit string) (string, bool) {
	at := strings.IndexByte(unit, '@')
	dot := strings.LastIndexByte(unit, '.')
	if at < 0 || dot < at+2 {
		return "", false
	}
	return unit[:at+1] + unit[dot:], true
}
