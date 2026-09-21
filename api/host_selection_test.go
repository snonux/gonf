package api

import (
	"context"
	"reflect"
	"testing"

	"github.com/snonux/gonf/internal/remote"
)

// everyForHostsHost lists all hosts of setupForHostsInventory's "all" cluster, the
// visit order of an unrestricted recording.
var everyForHostsHost = []string{"h1", "h1-wg", "h2", "h3"}

// selectionCase is one recording entry point and the hosts ForHosts must
// visit when a task on cluster "all" is recorded through it.
type selectionCase struct {
	name string
	run  func() error
	want []string
}

// runSelectionCase registers the inventory and an "iter" task on cluster
// "all", runs tc through a faked SSH transport and checks the visited hosts.
// It also checks that the selection does not outlive the call: a following
// plain RecordPlan visits every host again.
func runSelectionCase(t *testing.T, tc selectionCase) {
	t.Helper()
	setupForHostsInventory(t)
	visited := registerForHostsTask("iter", "all")
	calls := captureSSHSafe(t)
	if err := tc.run(); err != nil {
		t.Fatalf("%s: %v", tc.name, err)
	}
	if len(calls.all()) == 0 {
		t.Fatalf("%s opened no SSH connection", tc.name)
	}
	if got := visitedHosts(*visited); !reflect.DeepEqual(got, tc.want) {
		t.Fatalf("%s visited %v, want %v", tc.name, got, tc.want)
	}
	*visited = nil
	if _, err := RecordPlan("after", "", "iter"); err != nil {
		t.Fatal(err)
	}
	if got := visitedHosts(*visited); !reflect.DeepEqual(got, everyForHostsHost) {
		t.Fatalf("selection leaked past %s: visited %v", tc.name, got)
	}
}

// TestForHostsSelectionOnPushHostAndPushTo covers single-host pushes. An
// inventory push or exact destination selects the target plus its aliases;
// anything that is not an exact, unambiguous destination records every host.
func TestForHostsSelectionOnPushHostAndPushTo(t *testing.T) {
	cases := []selectionCase{
		{"PushHost of an alias keeps the primary name", func() error {
			return PushHost(MustHost("h1-wg"), "iter")
		}, []string{"h1", "h1-wg"}},
		{"PushHost", func() error { return PushHost(MustHost("h2"), "iter") }, []string{"h2"}},
		{"PushTo cli user@host", func() error {
			return PushTo(PushTarget{Host: "rex@h2.example"}, "p", "iter")
		}, []string{"h2"}},
		{"PushTo shared ssh host", func() error {
			return PushTo(PushTarget{Host: "h1.example"}, "p", "iter")
		}, []string{"h1", "h1-wg"}},
		{"PushTo explicit user skips the contradicting entry, keeps aliases", func() error {
			return PushTo(PushTarget{Host: "paul@h1.example"}, "p", "iter")
		}, []string{"h1", "h1-wg"}},
		{"PushTo explicit user contradicting the only entry", func() error {
			return PushTo(PushTarget{Host: "paul@h2.example"}, "p", "iter")
		}, everyForHostsHost},
		{"PushTo with ssh -p", func() error {
			return PushTo(PushTarget{Host: "h1.example", ExtraSSH: []string{"-p", "2222"}}, "p", "iter")
		}, everyForHostsHost},
		{"PushTo with ssh -o HostName=", func() error {
			return PushTo(PushTarget{Host: "h1.example", ExtraSSH: []string{"-o", "HostName=h2.example"}}, "p", "iter")
		}, everyForHostsHost},
		{"PushTo unknown destination", func() error {
			return PushTo(PushTarget{Host: "stranger.example"}, "p", "iter")
		}, everyForHostsHost},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { runSelectionCase(t, tc) })
	}
}

// TestForHostsSelectionOnClusterAndFleet covers fan-out pushes whose members
// include an alias: the alias's primary name must stay selected.
func TestForHostsSelectionOnClusterAndFleet(t *testing.T) {
	edge := []string{"h1", "h1-wg", "h2"}
	cases := []selectionCase{
		{"PushCluster", func() error { return PushCluster("edge", "iter") }, edge},
		{"PushFleet", func() error { return PushFleet("edge-fleet", "iter") }, edge},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { runSelectionCase(t, tc) })
	}
}

// TestForHostsSelectionOnPreviews covers every strict-preview entry point:
// previews narrow exactly like the matching pushes.
func TestForHostsSelectionOnPreviews(t *testing.T) {
	ctx := context.Background()
	cases := []selectionCase{
		{"PreviewHost", func() error { return PreviewHost(MustHost("h1-wg"), "iter") }, []string{"h1", "h1-wg"}},
		{"PreviewTo exact destination", func() error {
			return PreviewTo(PushTarget{Host: "rex@h2.example"}, "p", "iter")
		}, []string{"h2"}},
		{"PreviewTo with raw ssh args", func() error {
			return PreviewTo(PushTarget{Host: "rex@h2.example", ExtraSSH: []string{"-p", "2222"}}, "p", "iter")
		}, everyForHostsHost},
		{"PreviewClusterRun", func() error {
			return PreviewClusterRun(ctx, "edge", "", 0, remote.DefaultHostTimeout, "iter")
		}, []string{"h1", "h1-wg", "h2"}},
		{"PreviewFleetRun", func() error {
			return PreviewFleetRun(ctx, "edge-fleet", "", 0, remote.DefaultHostTimeout, "iter")
		}, []string{"h1", "h1-wg", "h2"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { runSelectionCase(t, tc) })
	}
}

// TestForHostsSelectionSameForEveryNameOfOneMachine: db and pi10 are one
// machine (10.0.0.5) whose live hostname may contain "pi10" and therefore
// "pi1". Pushing any of its names must visit pi1 too, exactly like the old
// loop's guards would have applied it there.
func TestForHostsSelectionSameForEveryNameOfOneMachine(t *testing.T) {
	pushes := map[string]func() error{
		"PushHost(db)":       func() error { return PushHost(MustHost("db"), "iter") },
		"PushHost(pi10)":     func() error { return PushHost(MustHost("pi10"), "iter") },
		"PushTo(10.0.0.5)":   func() error { return PushTo(PushTarget{Host: "10.0.0.5"}, "p", "iter") },
		"PushCluster(dbweb)": func() error { return PushCluster("dbweb", "iter") },
	}
	wants := map[string][]string{
		"PushCluster(dbweb)": {"db", "pi10", "pi1", "web"},
	}
	for name, push := range pushes {
		t.Run(name, func(t *testing.T) {
			resetForHostsState(t)
			v := WithValue(forHostsKey, [2]string{"x", "y"})
			db := Host("db", WithSSHHost("10.0.0.5"), v)
			web := Host("web", WithSSHHost("web.lan"), v)
			Cluster("all", db, Host("pi10", WithSSHHost("10.0.0.5"), v),
				Host("pi1", WithSSHHost("pi1.lan"), v), web)
			Cluster("dbweb", db, web)
			visited := registerForHostsTask("iter", "all")
			captureSSHSafe(t)
			if err := push(); err != nil {
				t.Fatal(err)
			}
			want, ok := wants[name]
			if !ok {
				want = []string{"db", "pi10", "pi1"}
			}
			if got := visitedHosts(*visited); !reflect.DeepEqual(got, want) {
				t.Fatalf("%s visited %v, want %v", name, got, want)
			}
		})
	}
}

// TestForHostsSelectionReachesAggregateChildren checks that an aggregate's
// child tasks record under the push's host selection, and that the child's
// own WithCluster (not the aggregate's) supplies the hosts.
func TestForHostsSelectionReachesAggregateChildren(t *testing.T) {
	setupForHostsInventory(t)
	visited := registerForHostsTask("child_iter", "all")
	Aggregate("parent", "", "^child_iter$")
	captureSSHSafe(t)
	if err := PushHost(MustHost("h3"), "parent"); err != nil {
		t.Fatal(err)
	}
	if want := []string{"h3=56"}; !reflect.DeepEqual(*visited, want) {
		t.Fatalf("visited = %v, want %v", *visited, want)
	}
}

// TestHostSelectionRestoredAfterPanic checks that recordPlanForHosts restores
// the previous selection even when a task body panics.
func TestHostSelectionRestoredAfterPanic(t *testing.T) {
	resetForHostsState(t)
	Task("boom", "", func() { panic("boom") })
	func() {
		defer func() { _ = recover() }()
		_, _ = recordPlanForHosts([]string{"only"}, "p", nil, "boom")
	}()
	if !hostSelected("anything") {
		t.Fatal("host selection leaked past a panicking recording")
	}
}
