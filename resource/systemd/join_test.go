package systemd

import (
	"reflect"
	"strings"
	"testing"

	opt "github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/internal/declerr"
	"github.com/snonux/gonf/internal/testseam"
	"github.com/snonux/gonf/resource"
)

// noop registers a do-nothing resource of kind/name and returns it.
func noop(kind, name string, deps ...string) resource.Resource {
	r, _ := resource.Register(kind, name, func() error { return nil }, deps...)
	return r
}

// TestJoinRegisteredReload pins which joins succeed: a same-bus registered
// reload gains exactly one dependency on the joiner (idempotently), while a
// missing reload, the other bus and a joiner that already depends on the
// reload leave the registered draft untouched.
func TestJoinRegisteredReload(t *testing.T) {
	for _, tc := range []struct {
		name       string
		reloadUser bool
		joinUser   bool
		joinerDeps func(reload resource.Resource) []string
		want       bool
	}{
		{name: "same user bus joins", reloadUser: true, joinUser: true, want: true},
		{name: "same system bus joins", want: true},
		{name: "user joiner does not join the system reload", joinUser: true},
		{name: "system joiner does not join the user reload", reloadUser: true},
		{name: "joiner depending on the reload does not join (cycle)", reloadUser: true, joinUser: true,
			joinerDeps: func(r resource.Resource) []string { return []string{r.ID()} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resource.ResetRepository()
			t.Cleanup(resource.ResetRepository)
			in := noop("File", "/u/a.service")
			ropts := []opt.DaemonReloadOption{opt.OnChange(in)}
			if tc.reloadUser {
				ropts = append(ropts, opt.WithUser)
			}
			reload := Present(ropts...)
			before := registeredDraft(t, reload.ID())
			var deps []string
			if tc.joinerDeps != nil {
				deps = tc.joinerDeps(reload)
			}
			joiner := noop("SystemdTimer", "job", deps...)

			if got := JoinRegisteredReload(tc.joinUser, joiner.ID()); got != tc.want {
				t.Fatalf("JoinRegisteredReload = %v, want %v", got, tc.want)
			}
			after := registeredDraft(t, reload.ID())
			if !tc.want {
				if !reflect.DeepEqual(after, before) {
					t.Fatalf("refused join changed the reload draft: %#v -> %#v", before, after)
				}
				return
			}
			wantDeps := []string{"File[/u/a.service]", "SystemdTimer[job]"}
			if !reflect.DeepEqual(after.Deps, wantDeps) || !reflect.DeepEqual(after.Watch, before.Watch) || !after.IfChanged {
				t.Fatalf("joined draft deps=%v watch=%v gated=%v, want deps %v and the gate unchanged",
					after.Deps, after.Watch, after.IfChanged, wantDeps)
			}
			if !JoinRegisteredReload(tc.joinUser, joiner.ID()) {
				t.Fatal("repeated join of the same joiner must still report joined")
			}
			if again := registeredDraft(t, reload.ID()); !reflect.DeepEqual(again.Deps, wantDeps) {
				t.Fatalf("repeated join duplicated the dep: %v", again.Deps)
			}
		})
	}
}

// TestJoinRegisteredReloadWithoutReload: a joiner alone (a standalone
// SystemdTimer) joins nothing and registers nothing.
func TestJoinRegisteredReloadWithoutReload(t *testing.T) {
	resource.ResetRepository()
	t.Cleanup(resource.ResetRepository)
	joiner := noop("SystemdTimer", "job")
	if JoinRegisteredReload(true, joiner.ID()) {
		t.Fatal("joined a reload that is not registered")
	}
	if n := len(resource.RegisteredPlanDrafts()); n != 0 {
		t.Fatalf("join without a reload recorded %d drafts", n)
	}
}

// TestGatedReloadCoalescesWithEarlierSameBusReload is the apply-time half of
// the join: a gated reload whose watched input changed before an earlier
// reload on the same bus is held, one whose input changed after it runs,
// and a reload on the other bus never counts as that earlier reload.
func TestGatedReloadCoalescesWithEarlierSameBusReload(t *testing.T) {
	if err := Require("DaemonReload"); err != nil {
		t.Skip(err)
	}
	for _, tc := range []struct {
		name  string
		notes [][2]string // id, "changed" in apply order before the reload
		want  bool
	}{
		{name: "input changed before an earlier user reload",
			notes: [][2]string{{"File[a]", "changed"}, {"DaemonReload[user]", "changed"}}},
		{name: "input changed after the earlier user reload", want: true,
			notes: [][2]string{{"DaemonReload[user]", "changed"}, {"File[a]", "changed"}}},
		{name: "earlier reload on the system bus does not coalesce", want: true,
			notes: [][2]string{{"File[a]", "changed"}, {"DaemonReload[system]", "changed"}}},
		{name: "no change at all", notes: [][2]string{{"DaemonReload[user]", "changed"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ran := false
			testseam.FakeSystemctl(t, func(string, ...string) (string, string, int, error) {
				ran = true
				return "", "", 0, nil
			})
			resource.ResetReport()
			t.Cleanup(resource.ResetReport)
			for _, n := range tc.notes {
				resource.Note(n[0], resource.StatusChanged)
			}
			if err := Ensure(opt.WithUser, opt.WatchChanges("File[a]")); err != nil {
				t.Fatalf("Ensure: %v", err)
			}
			if ran != tc.want {
				t.Fatalf("daemon-reload ran=%v, want %v", ran, tc.want)
			}
		})
	}
}

// TestMayManageUnit pins the conservative input-to-unit match that guards a
// join: a File by unit, template or drop-in directory name only (including
// the unit's dash-prefix drop-in directories and the bare type-wide one,
// dropinDirs), and every other kind (a SyncDir Directory, anything unknown)
// as possibly any unit.
func TestMayManageUnit(t *testing.T) {
	for _, tc := range []struct {
		id, unit string
		want     bool
	}{
		{"File[/etc/systemd/system/a.service]", "a.service", true},
		{"File[/etc/systemd/system/b.service]", "a.service", false},
		{"File[/etc/systemd/system/a.service.d/10-x.conf]", "a.service", true},
		{"File[/etc/systemd/system/b.service.d/10-x.conf]", "a.service", false},
		{"File[/etc/systemd/system/a@.service]", "a@x.service", true},
		{"File[/etc/systemd/system/a@.service.d/10-x.conf]", "a@x.service", true},
		{"File[/etc/systemd/system/a@.service]", "a.service", false},
		{"File[/usr/local/bin/a.service-helper]", "a.service", false},
		{"Directory[/home/u/.config/systemd/user]", "a.service", true},
		{"Command[whatever]", "a.service", true},
		// Dash-prefix drop-in directories (systemd.unit(5)): for
		// foo-bar.service, systemd also reads foo-.service.d/ (one
		// dash-prefix level, since the name has one '-').
		{"File[/etc/systemd/system/foo-.service.d/10-x.conf]", "foo-bar.service", true},
		// For foo-bar-baz.service, systemd checks every remaining
		// prefix level: foo-bar-.service.d/ and foo-.service.d/.
		{"File[/etc/systemd/system/foo-bar-.service.d/10-x.conf]", "foo-bar-baz.service", true},
		{"File[/etc/systemd/system/foo-.service.d/10-x.conf]", "foo-bar-baz.service", true},
		// The bare <type>.d directory applies to every unit of that
		// type, dash or no dash in its name.
		{"File[/etc/systemd/system/service.d/10-x.conf]", "foo-bar.service", true},
		{"File[/etc/systemd/system/service.d/10-x.conf]", "a.service", true},
		// Negative: a dash-prefix dir that is not actually a prefix
		// of the unit's name (systemd would not read it for this
		// unit) must not match.
		{"File[/etc/systemd/system/bar-.service.d/10-x.conf]", "foo-bar.service", false},
		// Negative: a.service has no '-', so it has no dash-prefix
		// directory beyond the bare type-wide one.
		{"File[/etc/systemd/system/a-.service.d/10-x.conf]", "a.service", false},
		// Negative: the bare type-wide directory of the wrong type
		// must not match.
		{"File[/etc/systemd/system/timer.d/10-x.conf]", "a.service", false},
		// Negative (zc2): a same-named service.d directory that is not
		// under any standard systemd unit search path is not a
		// drop-in directory systemd would ever read for a.service,
		// no matter its basename.
		{"File[/srv/data/service.d/x.conf]", "a.service", false},
		{"File[/home/u/notes/service.d/todo.conf]", "a.service", false},
		// Negative (zc2): same for a dash-prefix drop-in directory
		// outside a unit search path.
		{"File[/srv/data/foo-.service.d/10-x.conf]", "foo-bar.service", false},
		// Positive: the bare type-wide and dash-prefix directories
		// still match under a real search directory, including the
		// user one addressed by home (~/.config/systemd/user),
		// matched by suffix since the home directory varies.
		{"File[/home/paul/.config/systemd/user/service.d/x.conf]", "a.service", true},
		{"File[/home/paul/.config/systemd/user/foo-.service.d/x.conf]", "foo-bar.service", true},
		// Positive (dd2): $XDG_DATA_HOME/systemd/user (or its
		// ~/.local/share default) is a documented user unit load
		// directory too (systemd.unit(5), "Unit Load Path", Table 2),
		// matched by suffix like the .config case above.
		{"File[/home/u/.local/share/systemd/user/service.d/x.conf]", "a.service", true},
		// Positive (dd2): /etc/xdg/systemd/user is the documented
		// default for $XDG_CONFIG_DIRS/systemd/user, a fixed path
		// like /etc/systemd/user above.
		{"File[/etc/xdg/systemd/user/service.d/x.conf]", "a.service", true},
		// Positive (od2): a NON-default $XDG_CONFIG_HOME (or
		// $XDG_DATA_HOME) still ends in "/systemd/user", so the
		// widened suffix match catches it even though it is not one
		// of the documented ~/.config or ~/.local/share defaults
		// (dd2's literal-suffix match missed this).
		{"File[/home/u/cfg/systemd/user/service.d/x.conf]", "a.service", true},
		// Positive (od2): $XDG_RUNTIME_DIR/systemd/user
		// (/run/user/<uid>/systemd/user in its default form) is a
		// documented per-user unit load directory too
		// (systemd.unit(5), "Unit Load Path", Table 2) that the prior
		// literal-suffix match also missed; /run/systemd/user (the
		// SYSTEM path) was already in unitSearchDirs, but not this
		// per-user runtime one.
		{"File[/run/user/1000/systemd/user/service.d/x.conf]", "a.service", true},
		// Positive (pe2): the SYSTEM-side twin of od2's widening --
		// a drop-in written under an alternate root or container
		// rootfs (e.g. /mnt/newroot/... or /srv/chroot/...) still
		// ends in "/systemd/system", so the widened suffix match
		// catches it even though it is not one of the literal
		// unitSearchDirs entries (which can never enumerate every
		// possible alternate-root mount point).
		{"File[/mnt/newroot/etc/systemd/system/service.d/x.conf]", "a.service", true},
		// Negative (od2 regression guard): the widened suffix match
		// must not start matching the dbus-transient directories
		// (*.control, transient, generator[.early|.late]) dd2
		// deliberately excluded -- none of them end in
		// "/systemd/user" or "/systemd/system", so they stay
		// excluded before and after this widening (and pe2's).
		{"File[/run/systemd/transient/service.d/x.conf]", "a.service", false},
		{"File[/run/systemd/generator/service.d/x.conf]", "a.service", false},
		{"File[/run/systemd/generator.early/service.d/x.conf]", "a.service", false},
		{"File[/run/systemd/system/a.service.control/service.d/x.conf]", "a.service", false},
	} {
		if got := mayManageUnit(tc.id, tc.unit); got != tc.want {
			t.Errorf("mayManageUnit(%s, %s) = %v, want %v", tc.id, tc.unit, got, tc.want)
		}
	}
}

// TestJoinRegisteredReloadRefusesRelatedInput: a joiner whose related units
// may be one of the reload's inputs keeps its own reload (the registered
// draft is untouched); an unrelated unit still joins. A space-separated
// entry is checked unit by unit, as systemd splits it (cb2).
func TestJoinRegisteredReloadRefusesRelatedInput(t *testing.T) {
	for _, tc := range []struct {
		name    string
		input   string // a File path, or "" for a Directory input
		related []string
		want    bool
	}{
		{name: "wants a composition unit", input: "/u/a.service", related: []string{"a.service"}},
		{name: "after a composition drop-in", input: "/u/a.service.d/x.conf", related: []string{"network-online.target", "a.service"}},
		{name: "directory input may hold it", related: []string{"a.service"}},
		{name: "space-separated entry naming a composition unit", input: "/u/a.service", related: []string{"network-online.target a.service"}},
		{name: "tab- and space-separated entry naming a drop-in unit", input: "/u/a.service.d/x.conf", related: []string{"\tnetwork-online.target  a.service "}},
		{name: "unrelated unit joins", input: "/u/a.service", related: []string{"network-online.target"}, want: true},
		{name: "unrelated space-separated units join", input: "/u/a.service", related: []string{"network-online.target b.service", " "}, want: true},
		{name: "a unit named like a prefix of the entry joins", input: "/u/a.service", related: []string{"xa.service a.service.bak"}, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resource.ResetRepository()
			t.Cleanup(resource.ResetRepository)
			in := noop("Directory", "/u")
			if tc.input != "" {
				in = noop("File", tc.input)
			}
			reload := Present(opt.WithUser, opt.OnChange(in))
			before := registeredDraft(t, reload.ID())
			joiner := noop("SystemdTimer", "job")
			if got := JoinRegisteredReload(true, joiner.ID(), tc.related...); got != tc.want {
				t.Fatalf("JoinRegisteredReload = %v, want %v", got, tc.want)
			}
			if after := registeredDraft(t, reload.ID()); !tc.want && !reflect.DeepEqual(after, before) {
				t.Fatalf("refused join changed the reload draft: %#v -> %#v", before, after)
			}
		})
	}
}

// TestMergeRefusesJoinerRelatedToNewInput is the merge half of the related
// check (bb2): a joiner that references b.service joined while b was no
// input yet; a later same-bus declaration watching the File that installs
// b.service (by unit name, as an entry of a space-separated list, or as a
// drop-in) would make the merged reload load b only after the joiner
// started. The merge is refused with the "cannot merge" declaration error
// (internal/declerr) naming the joiner and the unit, and the registered
// reload keeps its draft.
func TestMergeRefusesJoinerRelatedToNewInput(t *testing.T) {
	for _, name := range []string{"unit", "listed", "drop-in"} {
		t.Run(name, func(t *testing.T) {
			resource.ResetForTest()
			t.Cleanup(resource.ResetForTest)
			related, input := "b.service", "/u/b.service"
			switch name {
			case "listed":
				related = "network-online.target b.service"
			case "drop-in":
				input = "/u/b.service.d/10-x.conf"
			}
			a := noop("File", "/u/a.service")
			reload := Present(opt.WithUser, opt.OnChange(a))
			joiner := noop("SystemdTimer", "job")
			if !JoinRegisteredReload(true, joiner.ID(), related) {
				t.Fatal("joiner unrelated to the reload's inputs at join time did not join")
			}
			before := registeredDraft(t, reload.ID())
			b := noop("File", input)
			Present(opt.WithUser, opt.OnChange(b))
			err := declerr.First()
			if err == nil {
				t.Fatalf("case %q merged without refusing the joiner", name)
			}
			for _, want := range []string{
				"DaemonReload[user]: cannot merge",
				"SystemdTimer[job]",
				"references b.service",
				"File[/u/a.service]",
			} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("case %s error misses %q: %v", name, want, err)
				}
			}
			if after := registeredDraft(t, reload.ID()); !reflect.DeepEqual(after, before) {
				t.Fatalf("refused merge changed the reload draft: %#v -> %#v", before, after)
			}
		})
	}
}

// TestMergeKeepsJoinerWithUnrelatedNewInput: the re-check at merge time
// only refuses a joiner whose related units may be one of the new inputs.
// A later declaration watching another unit, or a joiner without related
// units, merges as before: the reload stays ordered after the joiner and
// watches both inputs.
func TestMergeKeepsJoinerWithUnrelatedNewInput(t *testing.T) {
	for _, related := range [][]string{nil, {"network-online.target"}, {"c.service b.target"}} {
		resource.ResetRepository()
		t.Cleanup(resource.ResetRepository)
		a := noop("File", "/u/a.service")
		reload := Present(opt.WithUser, opt.OnChange(a))
		joiner := noop("SystemdTimer", "job")
		if !JoinRegisteredReload(true, joiner.ID(), related...) {
			t.Fatalf("related %v: join refused", related)
		}
		b := noop("File", "/u/b.service")
		Present(opt.WithUser, opt.OnChange(b))
		d := registeredDraft(t, reload.ID())
		wantDeps := []string{"File[/u/a.service]", "File[/u/b.service]", "SystemdTimer[job]"}
		wantWatch := []string{"File[/u/a.service]", "File[/u/b.service]"}
		if !reflect.DeepEqual(d.Deps, wantDeps) || !reflect.DeepEqual(d.Watch, wantWatch) {
			t.Fatalf("related %v: merged deps=%v watch=%v, want %v and %v", related, d.Deps, d.Watch, wantDeps, wantWatch)
		}
	}
}
