package systemd

import (
	"reflect"
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
