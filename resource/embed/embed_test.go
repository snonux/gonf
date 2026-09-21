package embed

import (
	"reflect"
	"slices"
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

// TestArmWithoutWatch pins the arming behind the legacy daemon-reload
// IfChanged option: the gate is armed but no ids are added.
func TestArmWithoutWatch(t *testing.T) {
	var c ChangeGate
	c.Arm()
	if !c.Gated || c.Watch != nil {
		t.Errorf("after Arm Gated=%t Watch=%#v, want true/nil", c.Gated, c.Watch)
	}
}

// TestChangeGateIsNotChangeGated pins that the embed does not provide
// SetIfChanged: if it did, every embedder would satisfy opt.ChangeGated and
// accept the daemon-reload-only IfChanged option through the type-erased
// option path instead of rejecting it.
func TestChangeGateIsNotChangeGated(t *testing.T) {
	if _, ok := any(&ChangeGate{}).(interface{ SetIfChanged() }); ok {
		t.Fatal("*ChangeGate implements SetIfChanged; embedders would accept IfChanged")
	}
}

// oracle returns a ChangeOracle reporting a change only for changedIDs and
// recording every id it is asked about in *asked.
func oracle(asked *[]string, changedIDs ...string) ChangeOracle {
	return func(ids ...string) bool {
		*asked = append(*asked, ids...)
		for _, id := range ids {
			if slices.Contains(changedIDs, id) {
				return true
			}
		}
		return false
	}
}

// TestHolds covers the hold predicate: an unarmed gate never holds (and
// does not consult the oracle), an armed gate holds when no watched id
// changed (including unknown ids and an empty watch list) and fires when
// one did.
func TestHolds(t *testing.T) {
	for _, tc := range []struct {
		name      string
		gate      ChangeGate
		changed   []string
		wantHold  bool
		wantAsked []string
	}{
		{name: "unarmed never holds", gate: ChangeGate{Watch: []string{"File[a]"}}},
		{name: "armed unchanged holds", gate: ChangeGate{Gated: true, Watch: []string{"File[a]"}},
			wantHold: true, wantAsked: []string{"File[a]"}},
		{name: "armed unknown id holds", gate: ChangeGate{Gated: true, Watch: []string{"File[nope]"}},
			changed: []string{"File[a]"}, wantHold: true, wantAsked: []string{"File[nope]"}},
		{name: "armed empty watch holds", gate: ChangeGate{Gated: true}, changed: []string{"File[a]"},
			wantHold: true},
		{name: "watched change fires", gate: ChangeGate{Gated: true, Watch: []string{"File[a]", "File[b]"}},
			changed: []string{"File[b]"}, wantAsked: []string{"File[a]", "File[b]"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var asked []string
			if got := tc.gate.Holds(oracle(&asked, tc.changed...)); got != tc.wantHold {
				t.Errorf("Holds = %t, want %t", got, tc.wantHold)
			}
			if !reflect.DeepEqual(asked, tc.wantAsked) {
				t.Errorf("oracle asked %#v, want %#v", asked, tc.wantAsked)
			}
		})
	}
}

// TestHoldsWatchingUsesSuppliedList pins that HoldsWatching consults the
// caller's effective watch list, not the embed's Watch field.
func TestHoldsWatchingUsesSuppliedList(t *testing.T) {
	c := ChangeGate{Gated: true, Watch: []string{"File[a]"}}
	var asked []string
	if !c.HoldsWatching(oracle(&asked, "File[a]"), []string{"File[b]"}) {
		t.Error("HoldsWatching fired on the embed's Watch instead of the supplied list")
	}
	if !reflect.DeepEqual(asked, []string{"File[b]"}) {
		t.Errorf("oracle asked %#v, want [File[b]]", asked)
	}
}

// TestDraftGate pins the plan-draft wiring: unarmed yields false/nil (the
// wire fields stay omitted), armed yields true plus an unaliased copy.
func TestDraftGate(t *testing.T) {
	off := ChangeGate{Watch: []string{"File[a]"}} // ids without arming are not recorded
	if gated, watch := off.DraftGate(); gated || watch != nil {
		t.Errorf("unarmed DraftGate = %t/%#v, want false/nil", gated, watch)
	}

	var on ChangeGate
	on.SetChangeWatch([]string{"File[a]"})
	gated, watch := on.DraftGate()
	if !gated || !reflect.DeepEqual(watch, []string{"File[a]"}) {
		t.Fatalf("armed DraftGate = %t/%#v", gated, watch)
	}
	on.Watch[0] = "File[mutated]"
	if watch[0] != "File[a]" {
		t.Errorf("DraftGate aliases Watch: %#v", watch)
	}

	var bare ChangeGate
	bare.Arm()
	if gated, watch := bare.DraftGate(); !gated || watch != nil {
		t.Errorf("armed-without-ids DraftGate = %t/%#v, want true/nil", gated, watch)
	}
}
