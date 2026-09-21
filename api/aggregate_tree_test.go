package api

import (
	"reflect"
	"slices"
	"testing"

	"github.com/snonux/gonf/plan"
)

// TestPatternAggregateSkipsAliasOfItself: a pattern that matches an alias of
// the aggregate must not recurse into the aggregate (it used to fail with
// "task recursion cycle detected: home -> home").
func TestPatternAggregateSkipsAliasOfItself(t *testing.T) {
	dir := resetAliasTest(t)
	a := fileTask(dir, "home_a")
	Aggregate("home", "", "^home")
	Alias("home_all", "", "home")

	if got := recordedFilePaths(t, "home"); !reflect.DeepEqual(got, []string{a}) {
		t.Fatalf("home ops = %v, want %v", got, []string{a})
	}
	if got := recordedFilePaths(t, "home_all"); !reflect.DeepEqual(got, []string{a}) {
		t.Fatalf("home_all ops = %v, want %v", got, []string{a})
	}
}

// TestAggregateCycleThroughAliasNamesAlias: aggregates record members under
// their public names, so a cycle through an alias names the alias.
func TestAggregateCycleThroughAliasNamesAlias(t *testing.T) {
	resetAliasTest(t)
	AggregateTasks("outer", "", "via_alias")
	Alias("via_alias", "", "inner")
	AggregateTasks("inner", "", "outer")

	wantErrContains(t, recordErr(t, "outer"),
		"task recursion cycle detected: outer -> via_alias -> inner -> outer")
}

// TestPatternAggregateSkipsAggregatesWithOperationalMembers pins the rule
// chosen for review issue 2: an AggregateTasks that transitively lists an
// Operational task is operational work for pattern aggregates, as is an
// alias of it; it stays callable by name.
func TestPatternAggregateSkipsAggregatesWithOperationalMembers(t *testing.T) {
	dir := resetAliasTest(t)
	setup := fileTask(dir, "fe_setup")
	invoke := fileTask(dir, "invoke", Operational())
	AggregateTasks("fe_acme", "", "invoke")
	AggregateTasks("fe_deep", "", "fe_acme")
	Alias("fe_acme_alias", "", "fe_acme")
	AggregateTasks("fe_plain", "", "fe_setup")
	Aggregate("fe", "", "^fe_")

	if got := recordedFilePaths(t, "fe"); !reflect.DeepEqual(got, []string{setup}) {
		t.Fatalf("pattern aggregate ops = %v, want only %v", got, setup)
	}
	if got := recordedFilePaths(t, "fe_deep"); !reflect.DeepEqual(got, []string{invoke}) {
		t.Fatalf("explicit aggregate by name ops = %v, want %v", got, invoke)
	}
}

// TestNestedAggregatesDeduplicateTransitively: a target reached through two
// nested aggregates (or an aggregate and a direct listing) records once, at
// its first position, while a cycle is still reported.
func TestNestedAggregatesDeduplicateTransitively(t *testing.T) {
	dir := resetAliasTest(t)
	x := fileTask(dir, "x")
	y := fileTask(dir, "y")
	z := fileTask(dir, "z")
	Alias("x_alias", "", "x")
	AggregateTasks("inner", "", "x", "y")
	AggregateTasks("inner_again", "", "y", "x_alias")
	AggregateTasks("outer", "", "inner", "x", "inner_again", "z")
	AggregateTasks("outer_first", "", "x_alias", "inner")
	Aggregate("p", "", "^(inner|inner_again|z)$")

	cases := map[string][]string{
		"outer":       {x, y, z},
		"outer_first": {x, y},
		"p":           {x, y, z},
	}
	for name, want := range cases {
		if got := recordedFilePaths(t, name); !reflect.DeepEqual(got, want) {
			t.Errorf("%s ops = %v, want %v", name, got, want)
		}
	}
}

// TestAggregateDedupeStopsAtTaskBodies: a plain task body may add its own
// envelope (here WhenLinux), so an aggregate it runs gets a fresh dedupe
// scope and records the member again inside that envelope.
func TestAggregateDedupeStopsAtTaskBodies(t *testing.T) {
	dir := resetAliasTest(t)
	x := fileTask(dir, "x")
	AggregateTasks("inner", "", "x")
	Task("linux_wrapper", "", func() { _ = Run("inner") }, WhenLinux())
	AggregateTasks("outer", "", "x", "linux_wrapper")

	ops, err := RecordPlan("scope", "", "outer")
	if err != nil {
		t.Fatal(err)
	}
	if got := filePaths(ops); !reflect.DeepEqual(got, []string{x, x}) {
		t.Fatalf("ops = %v, want x outside and inside the linux guard", got)
	}
	begin := slices.IndexFunc(ops, func(op plan.Op) bool { return op.Op == plan.KindWhenBegin })
	second := -1
	for i, op := range ops {
		if op.Op == plan.KindFile {
			second = i
		}
	}
	if begin < 0 || begin > second {
		t.Fatalf("second x op (%d) is not inside the when block (%d): %#v", second, begin, ops)
	}
}
