package api

import (
	"reflect"
	"testing"

	"github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/plan"
)

// onClusterRecipe is registered with OnCluster: Base is a plain body, Cron a
// ForHosts body, and Hosts records what ClusterHosts returns inside it.
type onClusterRecipe struct{ hosts *[]string }

func (onClusterRecipe) Base() { File("/tmp/on-cluster-base", options.WithContent("b")) }
func (r onClusterRecipe) Hosts() {
	*r.hosts = ClusterHosts()
}
func (onClusterRecipe) Cron() {
	ForHosts(forHostsKey, func(host string, w [2]string) {
		File("/tmp/on-cluster-"+host, options.WithContent(w[0]))
	})
}

// recordOps records names and fails the test on error.
func recordOps(t *testing.T, names ...string) []plan.Op {
	t.Helper()
	ops, err := RecordPlan("on-cluster", "", names...)
	if err != nil {
		t.Fatalf("RecordPlan(%v): %v", names, err)
	}
	return ops
}

// TestOnClusterGuardsEveryTask: OnCluster records one task-level
// hostname_contains guard over the members and keeps WithCluster's
// ClusterHosts/ForHosts behaviour inside the body.
func TestOnClusterGuardsEveryTask(t *testing.T) {
	setupForHostsInventory(t)
	var hosts []string
	RegisterMethods(onClusterRecipe{hosts: &hosts}, WithPrefix("oc_"), OnCluster("all"))

	members := []string{"h1", "h1-wg", "h2", "h3"}
	want := []plan.Predicate{{Fact: "hostname_contains", In: members}}
	for _, name := range []string{"oc_base", "oc_cron", "oc_hosts"} {
		ops := recordOps(t, name)
		if ops[1].Op != plan.KindWhenBegin || ops[1].ID != "when."+name || !reflect.DeepEqual(ops[1].All, want) {
			t.Fatalf("%s: first op = %#v, want the cluster guard %v", name, ops[1], want)
		}
	}
	if !reflect.DeepEqual(hosts, members) {
		t.Fatalf("ClusterHosts() in an OnCluster body = %v, want %v", hosts, members)
	}
	// ForHosts nests its per-host fragments inside the task guard (whenHosts
	// reads Eq, so the In guard itself shows as "").
	if got := whenHosts(recordOps(t, "oc_cron")); !reflect.DeepEqual(got, append([]string{""}, members...)) {
		t.Fatalf("ForHosts fragments = %v, want %v", got, members)
	}
}

// TestOnClusterMatchesWhenHostnameClusterHosts pins the guard's semantics:
// for every hostname, the OnCluster task applies exactly when some fragment
// of the WhenHostname(ClusterHosts(), ...) wrapper it replaces applies
// (case-insensitive substring, OR over the members).
func TestOnClusterMatchesWhenHostnameClusterHosts(t *testing.T) {
	setupForHostsInventory(t)
	RegisterMethods(onClusterRecipe{hosts: new([]string)}, WithPrefix("oc_"), OnCluster("all"))
	Task("wrapped", "", func() {
		WhenHostname(ClusterHosts(), func() { File("/tmp/on-cluster-base", options.WithContent("b")) })
	}, WithTaskCluster("all"))

	guard := recordOps(t, "oc_base")[1].All
	var fragments [][]plan.Predicate
	for _, op := range recordOps(t, "wrapped") {
		if op.Op == plan.KindWhenBegin {
			fragments = append(fragments, op.All)
		}
	}
	for _, hostname := range []string{"h1", "H2.lan", "xh3y", "h1-wg", "h4", "", "rocky"} {
		facts := plan.Facts{Hostname: hostname}
		got, err := plan.EvalPredicates(guard, facts)
		if err != nil {
			t.Fatal(err)
		}
		want := false
		for _, f := range fragments {
			ok, _ := plan.EvalPredicates(f, facts)
			want = want || ok
		}
		if got != want {
			t.Errorf("hostname %q: OnCluster guard = %v, WhenHostname(ClusterHosts()) = %v", hostname, got, want)
		}
	}
}

// TestOnClusterListing: off-cluster the task is listed destination-guarded
// (task 8h2's -list suffix), on a member host it is not; a one-host cluster
// records WhenHostname(host)'s exact Eq predicate.
func TestOnClusterListing(t *testing.T) {
	setupForHostsInventory(t)
	Cluster("solo", MustHost("h2"))
	RegisterMethods(onClusterRecipe{hosts: new([]string)}, WithPrefix("oc_"), OnCluster("all"))
	RegisterMethods(onClusterRecipe{hosts: new([]string)}, WithPrefix("solo_"), OnCluster("solo"))

	Activate(Facts{Hostname: "elsewhere"})
	if got := taskGuards(t)["oc_base"]; got != "hostname_contains=h1|h1-wg|h2|h3" {
		t.Fatalf("off-cluster DestinationGuard = %q", got)
	}
	Activate(Facts{Hostname: "h3.example"})
	if got := taskGuards(t)["oc_base"]; got != "" {
		t.Fatalf("member DestinationGuard = %q, want none", got)
	}
	want := []plan.Predicate{{Fact: "hostname_contains", Eq: "h2"}}
	if got := recordOps(t, "solo_base")[1].All; !reflect.DeepEqual(got, want) {
		t.Fatalf("one-host guard = %v, want %v", got, want)
	}
}

// TestOnClusterRefusals: an unknown cluster, or a WithCluster naming another
// cluster, is a declaration error that registers nothing of the struct.
func TestOnClusterRefusals(t *testing.T) {
	requireDeclErr(t, `RegisterMethods: OnCluster: Cluster "nope" is not registered`, func() {
		RegisterMethods(onClusterRecipe{hosts: new([]string)}, OnCluster("nope"))
	})
	if _, ok := findCandidate("base"); ok {
		t.Fatal("an unknown OnCluster cluster must register nothing")
	}
	requireDeclErr(t, `OnCluster("a") and WithCluster("b") name different clusters`, func() {
		RegisterMethods(onClusterRecipe{hosts: new([]string)}, OnCluster("a"), WithCluster("b"))
	})
}
