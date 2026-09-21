package api

import (
	"reflect"
	"testing"

	"github.com/snonux/gonf/plan"
)

// TestAliasOfClusterTaskKeepsClusterAndSelection pins that an alias of a
// WithTaskCluster target records inside the target's cluster (ForHosts sees
// the cluster's members), both directly and through recordPlanForHosts with
// a push host selection, and that its ops are the target's ops in both cases
// (also when an aggregate reaches the target through the alias).
func TestAliasOfClusterTaskKeepsClusterAndSelection(t *testing.T) {
	setupForHostsInventory(t)
	visited := registerForHostsTask("edge_task", "edge")
	Alias("edge_alias", "", "edge_task")
	AggregateTasks("edge_setup", "", "edge_alias", "edge_task")

	direct := recordForHosts(t, nil, "edge_task")
	for _, name := range []string{"edge_alias", "edge_setup"} {
		*visited = nil
		got := recordForHosts(t, nil, name)
		if !reflect.DeepEqual(got, direct) {
			t.Fatalf("%s ops differ from edge_task:\n got=%#v\nwant=%#v", name, got, direct)
		}
		if hosts := visitedHosts(*visited); !reflect.DeepEqual(hosts, []string{"h1-wg", "h2"}) {
			t.Fatalf("%s visited %v, want the edge cluster", name, hosts)
		}
	}
	if hosts := whenHosts(direct); !reflect.DeepEqual(hosts, []string{"h1-wg", "h2"}) {
		t.Fatalf("direct guards = %v", hosts)
	}

	selected := recordForHosts(t, []string{"h2"}, "edge_task")
	*visited = nil
	viaAlias := recordForHosts(t, []string{"h2"}, "edge_alias")
	if !reflect.DeepEqual(viaAlias, selected) {
		t.Fatalf("selected alias ops differ from target:\n got=%#v\nwant=%#v", viaAlias, selected)
	}
	if hosts := visitedHosts(*visited); !reflect.DeepEqual(hosts, []string{"h2"}) {
		t.Fatalf("selected alias visited %v, want [h2]", hosts)
	}
	if hosts := whenHosts(viaAlias); !reflect.DeepEqual(hosts, []string{"h2"}) {
		t.Fatalf("selected alias guards = %v, want [h2]", hosts)
	}
}

// recordForHosts records names with the push-path host selection hosts
// (nil: every host), without a blob store, and returns the ops.
func recordForHosts(t *testing.T, hosts []string, names ...string) []plan.Op {
	t.Helper()
	ops, err := recordPlanForHosts(hosts, "alias-cluster", nil, names...)
	if err != nil {
		t.Fatalf("recordPlanForHosts(%v, %v): %v", hosts, names, err)
	}
	return ops
}
