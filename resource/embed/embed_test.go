package embed

import (
	"reflect"
	"testing"
)

// TestSortedIDsNilSafe pins that dep-free resources lower to a nil Deps list,
// so plan.Op.Deps stays omitted on the wire instead of serializing an empty
// array.
func TestSortedIDsNilSafe(t *testing.T) {
	var d DependsOn
	if got := d.SortedIDs(); got != nil {
		t.Errorf("SortedIDs() = %#v, want nil", got)
	}
	d.IDs = []string{}
	if got := d.SortedIDs(); got != nil {
		t.Errorf("SortedIDs() on empty = %#v, want nil", got)
	}
}

// TestSortedIDsSortsAndDeduplicates mirrors the repository path's
// sortedDependsOn behaviour: IDs are unique and sorted for stable wire data,
// regardless of insertion order.
func TestSortedIDsSortsAndDedupes(t *testing.T) {
	d := DependsOn{IDs: []string{"File[c]", "File[a]", "File[b]", "File[a]"}}

	got := d.SortedIDs()
	want := []string{"File[a]", "File[b]", "File[c]"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("SortedIDs() = %#v, want %#v", got, want)
	}
	// The helper must not mutate the accumulated IDs.
	if !reflect.DeepEqual(d.IDs, []string{"File[c]", "File[a]", "File[b]", "File[a]"}) {
		t.Errorf("SortedIDs mutated IDs: %#v", d.IDs)
	}
}

// TestChangeGateDisarmedByDefault pins that a zero ChangeGate is inert: the
// gated action runs unconditionally and no ids are watched.
func TestChangeGateDisarmedByDefault(t *testing.T) {
	var c ChangeGate
	if c.Gated {
		t.Error("zero ChangeGate.Gated = true, want false")
	}
	if len(c.Watch) != 0 {
		t.Errorf("zero ChangeGate.Watch = %#v, want empty", c.Watch)
	}
}

// TestSetChangeWatchArmsAndAccumulates pins SetChangeWatch's contract: it
// arms the gate and accumulates watched ids across calls (OnChange may be
// applied more than once, and multi-resource fan-in passes several ids in
// one call).
func TestSetChangeWatchArmsAndAccumulates(t *testing.T) {
	var c ChangeGate
	c.SetChangeWatch([]string{"File[a]", "File[b]"})
	if !c.Gated {
		t.Fatal("SetChangeWatch did not arm the gate")
	}
	c.SetChangeWatch([]string{"File[c]"})
	want := []string{"File[a]", "File[b]", "File[c]"}
	if !reflect.DeepEqual(c.Watch, want) {
		t.Errorf("Watch = %#v, want %#v", c.Watch, want)
	}
}
