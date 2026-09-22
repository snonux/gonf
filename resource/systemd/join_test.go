package systemd

import (
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	opt "github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/resource"
)

// noop registers a do-nothing resource of kind/name and returns it.
func noop(kind, name string, deps ...string) resource.Resource {
	return resource.Register(kind, name, resource.ApplierFunc(func() error { return nil }), deps...)
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
			old := runCmd
			t.Cleanup(func() { runCmd = old })
			ran := false
			runCmd = func(string, ...string) (string, string, int, error) {
				ran = true
				return "", "", 0, nil
			}
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
// join: a File by unit, template or drop-in directory name only, and every
// other kind (a SyncDir Directory, anything unknown) as possibly any unit.
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
	} {
		if got := mayManageUnit(tc.id, tc.unit); got != tc.want {
			t.Errorf("mayManageUnit(%s, %s) = %v, want %v", tc.id, tc.unit, got, tc.want)
		}
	}
}

// TestJoinRegisteredReloadRefusesRelatedInput: a joiner whose related units
// may be one of the reload's inputs keeps its own reload (the registered
// draft is untouched); an unrelated unit still joins.
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
		{name: "unrelated unit joins", input: "/u/a.service", related: []string{"network-online.target"}, want: true},
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
// started. The merge is refused with the fail-fast "cannot merge" error
// naming the joiner and the unit. logger.Fatal exits, so each case runs in
// a helper process.
func TestMergeRefusesJoinerRelatedToNewInput(t *testing.T) {
	for _, name := range []string{"unit", "drop-in"} {
		t.Run(name, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestMergeRefusesJoinerFatalHelperProcess$", "-test.timeout=60s")
			cmd.Env = append(os.Environ(), "GONF_SYSTEMD_JOINER_MERGE_FATAL="+name)
			out, err := cmd.CombinedOutput()
			if err == nil {
				t.Fatalf("case %s exited 0; output:\n%s", name, out)
			}
			for _, want := range []string{
				"DaemonReload[user]: cannot merge",
				"SystemdTimer[job]",
				"references b.service",
				"File[/u/a.service]",
			} {
				if !strings.Contains(string(out), want) {
					t.Fatalf("case %s output misses %q:\n%s", name, want, out)
				}
			}
		})
	}
}

// TestMergeRefusesJoinerFatalHelperProcess is the helper process for
// TestMergeRefusesJoinerRelatedToNewInput; it must never exit 0.
func TestMergeRefusesJoinerFatalHelperProcess(t *testing.T) {
	name := os.Getenv("GONF_SYSTEMD_JOINER_MERGE_FATAL")
	if name == "" {
		return
	}
	resource.ResetRepository()
	related, input := "b.service", "/u/b.service"
	switch name {
	case "listed":
		related = "network-online.target b.service"
	case "drop-in":
		input = "/u/b.service.d/10-x.conf"
	}
	a := noop("File", "/u/a.service")
	Present(opt.WithUser, opt.OnChange(a))
	joiner := noop("SystemdTimer", "job")
	if !JoinRegisteredReload(true, joiner.ID(), related) {
		t.Fatal("joiner unrelated to the reload's inputs at join time did not join")
	}
	b := noop("File", input)
	Present(opt.WithUser, opt.OnChange(b))
	t.Fatalf("case %q merged without refusing the joiner", name)
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
