package api

import (
	"reflect"
	"testing"

	"github.com/snonux/gonf/internal/declerr"
	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
)

// Task 8b regression tests: a blank hostname fragment in WhenHostnameIn or
// WhenHostnameContains used to lower to hostname_contains "" — which every
// hostname contains — so the guarded task applied on every destination.

// TestHostnameGuardRefusesBlankFragment: every blank spelling is a
// declaration error at the recipe line, and the task stays fail-safe even
// once the sticky error is cleared: it gets no serializable guard, is never
// activated, and naming it fails the record instead of recording the body
// unguarded.
func TestHostnameGuardRefusesBlankFragment(t *testing.T) {
	cases := []struct {
		name string
		opt  func() TaskOption
		want string
	}{
		{"contains empty", func() TaskOption { return WhenHostnameContains("") }, `WhenHostnameContains: hostname fragment 1 ("") must not be empty`},
		{"contains whitespace", func() TaskOption { return WhenHostnameContains(" \t") }, `WhenHostnameContains: hostname fragment 1 (" \t") must not be empty`},
		{"in no hosts", func() TaskOption { return WhenHostnameIn() }, "WhenHostnameIn: no hosts"},
		{"in single empty", func() TaskOption { return WhenHostnameIn("") }, `WhenHostnameIn: hostname fragment 1 ("") must not be empty`},
		{"in single whitespace", func() TaskOption { return WhenHostnameIn("  ") }, `WhenHostnameIn: hostname fragment 1 ("  ") must not be empty`},
		{"in valid then empty", func() TaskOption { return WhenHostnameIn("f0", "") }, `WhenHostnameIn: hostname fragment 2 ("") must not be empty`},
		{"in empty then valid", func() TaskOption { return WhenHostnameIn("", "f1") }, `WhenHostnameIn: hostname fragment 1 ("") must not be empty`},
		{"in valid then whitespace", func() TaskOption { return WhenHostnameIn("f0", "f1", " ") }, `WhenHostnameIn: hostname fragment 3 (" ") must not be empty`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			var path string
			requireDeclErr(t, tc.want, func() { path = fileTask(dir, "guarded", tc.opt()) })

			c, ok := findCandidate("guarded")
			if !ok {
				t.Fatal("the task itself must still be queued (only its guard is refused)")
			}
			if len(c.planWhen) != 0 {
				t.Fatalf("a refused hostname guard must not lower to a plan predicate, got %v", c.planWhen)
			}

			// Clear the sticky refusal the way a library embedder would
			// (resource.ResetDeclarationError): the never-holding guard alone
			// must still keep the body from recording.
			if err := resource.ResetDeclarationError(); err == nil {
				t.Fatal("ResetDeclarationError returned nil, want the reported error")
			}
			Activate(DetectFacts())
			for _, info := range Tasks() {
				if info.Name == "guarded" {
					t.Fatal("a task with a refused hostname guard must never be activated")
				}
			}
			if ops, err := RecordPlan("8b", "", "guarded"); err == nil {
				t.Fatalf("RecordPlan must refuse the refused-guard task, recorded %v", filePaths(ops))
			}
			if exists(path) {
				t.Fatalf("%s written despite the refused guard", path)
			}
		})
	}
}

// TestHostnameGuardValidFragmentsGuardOnDestination: non-blank fragments
// keep their meaning end to end — recorded as the same predicate as before
// and applied only where the destination's hostname contains one of them
// (case insensitive).
func TestHostnameGuardValidFragmentsGuardOnDestination(t *testing.T) {
	dir := resetAliasTest(t)
	in := fileTask(dir, "in_pair", WhenHostnameIn("f0", "F1"))
	one := fileTask(dir, "contains_rocky", WhenHostnameContains("rocky"))
	if err := declerr.First(); err != nil {
		t.Fatalf("valid hostname guards reported %v", err)
	}

	ops, err := RecordPlan("8b", "", "in_pair", "contains_rocky")
	if err != nil {
		t.Fatal(err)
	}
	guards := map[string][]plan.Predicate{}
	for _, op := range ops {
		if op.Op == plan.KindWhenBegin {
			guards[op.ID] = op.All
		}
	}
	want := map[string][]plan.Predicate{
		"when.in_pair":        {{Fact: "hostname_contains", In: []string{"f0", "F1"}}},
		"when.contains_rocky": {{Fact: "hostname_contains", Eq: "rocky"}},
	}
	if !reflect.DeepEqual(guards, want) {
		t.Fatalf("recorded guards = %#v, want %#v", guards, want)
	}

	applyWithHostname(t, ops, "earth.lan")
	if exists(in) || exists(one) {
		t.Fatalf("non-matching destination applied guarded tasks: in=%v rocky=%v", exists(in), exists(one))
	}
	applyWithHostname(t, ops, "f1.lan.example")
	if !exists(in) || exists(one) {
		t.Fatalf("destination f1: in written=%v (want true), rocky written=%v (want false)", exists(in), exists(one))
	}
	applyWithHostname(t, ops, "ROCKY")
	if !exists(one) {
		t.Fatal("destination ROCKY did not apply the WhenHostnameContains(rocky) task")
	}
}

// TestHostnameGuardCopiesHosts: the option snapshots its host list when it
// is built, so a caller reusing its slice cannot change (or blank) the
// guard afterwards.
func TestHostnameGuardCopiesHosts(t *testing.T) {
	resetAliasTest(t)
	hosts := []string{"f0", "f1"}
	opt := WhenHostnameIn(hosts...)
	hosts[1] = ""
	Task("copied", "", func() {}, opt)
	if err := declerr.First(); err != nil {
		t.Fatalf("mutating the caller's slice after WhenHostnameIn leaked into the guard: %v", err)
	}
	c, _ := findCandidate("copied")
	if want := []plan.Predicate{{Fact: "hostname_contains", In: []string{"f0", "f1"}}}; !reflect.DeepEqual(c.planWhen, want) {
		t.Fatalf("guard = %v, want %v", c.planWhen, want)
	}
}
