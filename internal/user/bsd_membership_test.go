package user

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/snonux/gonf/resource"
)

// fakeBSDGroupDB is a stateful stand-in for the OpenBSD/NetBSD account tools.
// It models one existing account and the group file, and applies usermod -G
// with either reading: replace (drop every explicit membership, then add the
// listed groups) or append (add the listed groups, skipping those that
// already list the user). The skip mirrors an unverified recollection of the
// shared BSD user.c append step, pending native verification (task y42).
// It lets the tests prove that no membership is dropped and that a second
// run is a no-op, without touching real users or groups.
type fakeBSDGroupDB struct {
	t         *testing.T
	user      string
	primary   string
	members   map[string][]string
	replace   bool
	mutations []string
}

func newFakeBSDGroupDB(t *testing.T, replace bool) *fakeBSDGroupDB {
	t.Helper()
	return &fakeBSDGroupDB{
		t:       t,
		user:    "svc",
		primary: "svc",
		replace: replace,
		members: map[string][]string{
			"svc":   nil,
			"wheel": {"root", "svc"},
			"video": {"svc"},
			"audio": nil,
			"staff": {"root", "svcx"},
		},
	}
}

func (f *fakeBSDGroupDB) run(command string, args ...string) (string, string, int, error) {
	f.t.Helper()
	switch key := command + " " + strings.Join(args, " "); {
	case key == "getent passwd "+f.user:
		return f.user + ":*:1001:1001::/home/" + f.user + ":/bin/sh\n", "", 0, nil
	case key == "getent group":
		return f.enumerate(), "", 0, nil
	case command == "getent" && len(args) == 2 && args[0] == "group":
		if _, ok := f.members[args[1]]; ok {
			return args[1] + ":*:1:\n", "", 0, nil
		}
		return "", "", 2, nil
	case key == "id -Gn "+f.user:
		return strings.Join(append([]string{f.primary}, f.groupsOf()...), " ") + "\n", "", 0, nil
	case command == "groupadd" && len(args) == 1:
		f.mutations = append(f.mutations, key)
		f.members[args[0]] = nil
		return "", "", 0, nil
	case command == "usermod" && len(args) == 3 && args[0] == "-G" && args[2] == f.user:
		f.mutations = append(f.mutations, key)
		return f.usermodG(strings.Split(args[1], ","))
	}
	f.t.Fatalf("unexpected command %s %v", command, args)
	return "", "", 0, nil
}

func (f *fakeBSDGroupDB) usermodG(groups []string) (string, string, int, error) {
	for _, group := range groups {
		if _, ok := f.members[group]; !ok {
			return "", "usermod: unknown group " + group, 1, nil
		}
	}
	if f.replace {
		for group, members := range f.members {
			f.members[group] = slices.DeleteFunc(members, func(m string) bool { return m == f.user })
		}
	}
	for _, group := range groups {
		if !slices.Contains(f.members[group], f.user) {
			f.members[group] = append(f.members[group], f.user)
		}
	}
	return "", "", 0, nil
}

// groupsOf returns the sorted groups that list the account explicitly.
func (f *fakeBSDGroupDB) groupsOf() []string {
	var groups []string
	for group, members := range f.members {
		if slices.Contains(members, f.user) {
			groups = append(groups, group)
		}
	}
	sort.Strings(groups)
	return groups
}

func (f *fakeBSDGroupDB) enumerate() string {
	names := make([]string, 0, len(f.members))
	for group := range f.members {
		names = append(names, group)
	}
	sort.Strings(names)
	var out strings.Builder
	for i, group := range names {
		fmt.Fprintf(&out, "%s:*:%d:%s\n", group, 100+i, strings.Join(f.members[group], ","))
	}
	return out.String()
}

// TestBSDMembershipsNeverDropExistingGroups is the cross-reading conformance
// check: OpenBSD is exercised under its documented append semantics, NetBSD
// under both readings because its manual does not say which applies. Every
// pre-existing membership must survive, only the missing groups are added,
// no member list gains a duplicate, and a second run mutates nothing.
func TestBSDMembershipsNeverDropExistingGroups(t *testing.T) {
	cases := []struct {
		name    string
		replace bool
		backend func(Runner) interface{ Ensure(DesiredUser) error }
		usermod string
	}{
		{"openbsd append", false, func(r Runner) interface{ Ensure(DesiredUser) error } { return NewOpenBSD(r) }, "usermod -G audio,games svc"},
		{"netbsd append", false, func(r Runner) interface{ Ensure(DesiredUser) error } { return NewNetBSD(r) }, "usermod -G audio,games,video,wheel svc"},
		{"netbsd replace", true, func(r Runner) interface{ Ensure(DesiredUser) error } { return NewNetBSD(r) }, "usermod -G audio,games,video,wheel svc"},
	}
	want := DesiredUser{Name: "svc", SupplementaryGroups: []string{"wheel", "audio", "games"}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := newFakeBSDGroupDB(t, tc.replace)
			if err := tc.backend(db.run).Ensure(want); err != nil {
				t.Fatalf("first Ensure() = %v", err)
			}
			wantMutations := []string{"groupadd games", tc.usermod}
			if !slices.Equal(db.mutations, wantMutations) {
				t.Fatalf("mutations = %q, want %q", db.mutations, wantMutations)
			}
			if got, wantGroups := db.groupsOf(), []string{"audio", "games", "video", "wheel"}; !slices.Equal(got, wantGroups) {
				t.Fatalf("memberships = %v, want %v", got, wantGroups)
			}
			assertNoDuplicateMembers(t, db)
			if !slices.Equal(db.members["staff"], []string{"root", "svcx"}) {
				t.Fatalf("unrelated group staff changed: %v", db.members["staff"])
			}

			if err := tc.backend(db.run).Ensure(want); err != nil {
				t.Fatalf("second Ensure() = %v", err)
			}
			if len(db.mutations) != len(wantMutations) {
				t.Fatalf("second run mutated: %q", db.mutations[len(wantMutations):])
			}
		})
	}
}

func assertNoDuplicateMembers(t *testing.T, db *fakeBSDGroupDB) {
	t.Helper()
	for group, members := range db.members {
		if len(slices.Compact(slices.Sorted(slices.Values(members)))) != len(members) {
			t.Fatalf("group %s has duplicate members %v", group, members)
		}
	}
}

// TestBSDMembershipDryRunProbesWithoutMutating checks that a dry run of an
// existing-account update runs every probe (including NetBSD's group
// enumeration) but never usermod, and still reports the account as changed.
func TestBSDMembershipDryRunProbesWithoutMutating(t *testing.T) {
	cases := map[string]struct {
		backend func(Runner) interface{ Ensure(DesiredUser) error }
		union   []bsdCall
	}{
		"openbsd": {func(r Runner) interface{ Ensure(DesiredUser) error } { return NewOpenBSD(r) }, nil},
		"netbsd": {func(r Runner) interface{ Ensure(DesiredUser) error } { return NewNetBSD(r) },
			[]bsdCall{{command: "getent", args: []string{"group"}, stdout: "wheel:*:0:svc\n"}}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			withDryRun(t)
			calls := membershipProbeCalls(tc.union)
			calls = append(calls, bsdCall{command: "getent", args: []string{"group", "audio"}})
			err := tc.backend(scriptedBSDRunner(t, calls)).Ensure(DesiredUser{Name: "svc", SupplementaryGroups: []string{"audio"}})
			if err != nil {
				t.Fatalf("Ensure() = %v", err)
			}
			if !resource.AnyChanged("User[svc]") {
				t.Fatal("dry-run did not report the suppressed usermod")
			}
		})
	}
}

func withDryRun(t *testing.T) {
	t.Helper()
	original := resource.DryRun()
	resource.ResetReport()
	resource.SetDryRun(true)
	t.Cleanup(func() {
		resource.SetDryRun(original)
		resource.ResetReport()
	})
}

// TestBSDMembershipUsermodFailureSurfaces checks that a failing usermod -G is
// returned with its argv on both platforms rather than swallowed.
func TestBSDMembershipUsermodFailureSurfaces(t *testing.T) {
	cases := map[string]struct {
		backend func(Runner) interface{ Ensure(DesiredUser) error }
		extra   []bsdCall
		argv    string
	}{
		"openbsd": {func(r Runner) interface{ Ensure(DesiredUser) error } { return NewOpenBSD(r) }, nil, "usermod -G audio svc"},
		"netbsd": {func(r Runner) interface{ Ensure(DesiredUser) error } { return NewNetBSD(r) },
			[]bsdCall{{command: "getent", args: []string{"group"}, stdout: "wheel:*:0:svc\n"}}, "usermod -G audio,wheel svc"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			calls := membershipProbeCalls(tc.extra)
			calls = append(calls, bsdCall{command: "getent", args: []string{"group", "audio"}})
			args := strings.Fields(tc.argv)[1:]
			calls = append(calls, bsdCall{command: "usermod", args: args, code: 1, stderr: "usermod: can't change"})
			err := tc.backend(scriptedBSDRunner(t, calls)).Ensure(DesiredUser{Name: "svc", SupplementaryGroups: []string{"audio"}})
			if err == nil || !strings.Contains(err.Error(), tc.argv+" failed (exit 1)") {
				t.Fatalf("Ensure() = %v", err)
			}
		})
	}
}

// TestNetBSDMembershipUnionErrorsStopBeforeMutation covers every way the
// NetBSD union can fail; each must stop before any groupadd or usermod runs,
// so no partial or truncated membership list is written and no group is
// left behind by a refused update.
func TestNetBSDMembershipUnionErrorsStopBeforeMutation(t *testing.T) {
	cases := map[string]struct {
		enumerate bsdCall
		errText   string
	}{
		"enumeration fails": {bsdCall{command: "getent", args: []string{"group"}, code: 1, stderr: "boom"}, "getent group failed (exit 1)"},
		"runner error":      {bsdCall{command: "getent", args: []string{"group"}, err: fmt.Errorf("timeout")}, "getent group: timeout"},
		"malformed entry":   {bsdCall{command: "getent", args: []string{"group"}, stdout: "wheel:*:0\n"}, "malformed group entry"},
		"odd group name":    {bsdCall{command: "getent", args: []string{"group"}, stdout: "bad name:*:5:svc\n"}, `"bad name" contains whitespace`},
		"union over limit":  {bsdCall{command: "getent", args: []string{"group"}, stdout: explicitGroupLines(maxBSDSupplementaryGroups)}, "needs 17 supplementary groups; at most 16"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			// The scripted runner fails on any further command, so neither
			// the audio group probe nor groupadd nor usermod may follow.
			err := NewNetBSD(scriptedBSDRunner(t, []bsdCall{
				{command: "getent", args: []string{"passwd", "svc"}},
				{command: "id", args: []string{"-Gn", "svc"}, stdout: "svc\n"},
				tc.enumerate,
			})).Ensure(DesiredUser{Name: "svc", SupplementaryGroups: []string{"audio"}})
			if err == nil || !strings.Contains(err.Error(), tc.errText) {
				t.Fatalf("Ensure() = %v, want error containing %q", err, tc.errText)
			}
		})
	}
}

// explicitGroupLines returns count getent group lines that each list svc.
func explicitGroupLines(count int) string {
	var out strings.Builder
	for i, group := range bsdGroupNames(count) {
		fmt.Fprintf(&out, "%s:*:%d:svc\n", group, 2000+i)
	}
	return out.String()
}

// TestBSDMembershipRejectsOddGroupNamesBeforeAnyCommand keeps the existing
// name validation in front of the membership path on both platforms.
func TestBSDMembershipRejectsOddGroupNamesBeforeAnyCommand(t *testing.T) {
	backends := map[string]func(Runner) interface{ Ensure(DesiredUser) error }{
		"openbsd": func(r Runner) interface{ Ensure(DesiredUser) error } { return NewOpenBSD(r) },
		"netbsd":  func(r Runner) interface{ Ensure(DesiredUser) error } { return NewNetBSD(r) },
	}
	for name, backend := range backends {
		for _, group := range []string{"a,b", "a b", "-G", "a\tb", ""} {
			t.Run(name+"/"+group, func(t *testing.T) {
				err := backend(scriptedBSDRunner(t, nil)).Ensure(DesiredUser{Name: "svc", SupplementaryGroups: []string{group}})
				if err == nil || !strings.Contains(err.Error(), "supplementary group name") {
					t.Fatalf("Ensure() = %v", err)
				}
			})
		}
	}
}

// membershipProbeCalls is the probe prefix of an existing-account update of
// svc (currently in wheel): the account probe, id -Gn, then any union probes.
// Every probe runs before the first group probe or groupadd.
func membershipProbeCalls(unionProbes []bsdCall) []bsdCall {
	calls := []bsdCall{
		{command: "getent", args: []string{"passwd", "svc"}},
		{command: "id", args: []string{"-Gn", "svc"}, stdout: "svc wheel\n"},
	}
	return append(calls, unionProbes...)
}

// TestNetBSDRefusedUnionCreatesNoGroup uses the stateful group database with
// a requested group (games) that does not exist yet. When the union is
// refused, because it exceeds the limit or the database holds an unsafe
// name, the refusal must come before groupadd games, so the run mutates
// nothing and a retry does not accumulate half-applied state.
func TestNetBSDRefusedUnionCreatesNoGroup(t *testing.T) {
	cases := map[string]struct {
		extra   []string
		errText string
	}{
		"union over limit": {bsdGroupNames(maxBSDSupplementaryGroups - 1), "needs 17 supplementary groups"},
		"odd name in db":   {[]string{"bad name"}, `"bad name" contains whitespace`},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			db := newFakeBSDGroupDB(t, true)
			delete(db.members, "video")
			for _, group := range tc.extra {
				db.members[group] = []string{"svc"}
			}
			if _, ok := db.members["games"]; ok {
				t.Fatal("fixture: games must not exist yet")
			}
			err := NewNetBSD(db.run).Ensure(DesiredUser{Name: "svc", SupplementaryGroups: []string{"games"}})
			if err == nil || !strings.Contains(err.Error(), tc.errText) {
				t.Fatalf("Ensure() = %v, want error containing %q", err, tc.errText)
			}
			if len(db.mutations) != 0 {
				t.Fatalf("refused update mutated: %q", db.mutations)
			}
		})
	}
}
