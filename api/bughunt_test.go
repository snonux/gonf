package api

import (
	"reflect"
	"testing"

	"github.com/snonux/gonf/internal/inventory"
)

// Needs(T.Method) must resolve to the exact task under the dependent's
// prefix, not to another registration whose prefix merely starts with it.
func TestNeedsMethodPrefixOverlap(t *testing.T) {
	resetForHostsState(t)
	RegisterMethods(otherRecipe{}, WithPrefix("a_b_"))
	RegisterMethods(otherRecipe{}, WithPrefix("a_"))
	Task("a_user", "", func() {}, Needs(otherRecipe.Base), needsPrefix("a_"))
	c, _ := findCandidate("a_user")
	if got := c.resolvedNeeds(); !reflect.DeepEqual(got, []string{"a_base"}) {
		t.Fatalf("a_user needs = %v, want [a_base]", got)
	}
}

// A local run on a machine matched by WithHostnameMatch (not by the host's
// name) must still record that host's EachHost body.
func TestLocalSelectionUsesHostnameMatch(t *testing.T) {
	resetForHostsState(t)
	web := Host("web", WithHostnameMatch("srv-17"), WithData(upsClient{Server: "web"}))
	Cluster("c", web)
	var seen []string
	Task("each", "", func() {
		EachHost(func(c upsClient) { seen = append(seen, c.Server) })
	}, WithTaskCluster("c"))
	sel := inventory.SelectionForLocalHostname("srv-17.lan")
	if !reflect.DeepEqual(sel, []string{"web"}) {
		t.Fatalf("selection = %v, want [web]", sel)
	}
	if _, err := recordPlanForHosts(sel, "local", nil, "each"); err != nil {
		t.Fatal(err)
	}
	if len(seen) == 0 {
		t.Fatal("EachHost body skipped on the matching machine")
	}
}
