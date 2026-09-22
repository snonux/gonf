package options

import (
	"reflect"
	"testing"

	"github.com/snonux/gonf/resource"
)

// TestDependsOnExpandsAndPreservesOrder pins DependsOn's expansion: a Multi
// contributes each member, arguments keep their order, and an empty call or
// empty Multi records nothing (it is valid, unlike an empty OnChange).
func TestDependsOnExpandsAndPreservesOrder(t *testing.T) {
	multi := resource.Multi{
		resource.Resource{Type: "Directory", Name: "/b"},
		resource.Resource{Type: "Package", Name: "c"},
	}
	tests := []struct {
		name string
		opt  AllResourceOption
		want []string
	}{
		{"no dependencies", DependsOn(), nil},
		{"empty multi", DependsOn(resource.Multi(nil)), nil},
		{"single", DependsOn(fileA), []string{"File[a]"}},
		{"single then multi", DependsOn(fileA, multi), []string{"File[a]", "Directory[/b]", "Package[c]"}},
		{"multi then single", DependsOn(multi, fileA), []string{"Directory[/b]", "Package[c]", "File[a]"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			target := &recorder{}
			tt.opt.Apply(target)
			if got := dependencyIDs(target); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("dependencies = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestOnChangeWatchesAndOrdersEveryMember checks OnChange arms the gate with
// the full expanded watch list before adding the same IDs as dependency
// edges, so the watched resources also apply first.
func TestOnChangeWatchesAndOrdersEveryMember(t *testing.T) {
	target := &recorder{}
	OnChange(fileA, resource.Multi{resource.Resource{Type: "Package", Name: "p"}}).Apply(target)
	want := []setterCall{
		{method: "SetChangeWatch", value: []string{"File[a]", "Package[p]"}},
		{method: "AddDependency", value: "File[a]"},
		{method: "AddDependency", value: "Package[p]"},
	}
	if !reflect.DeepEqual(target.calls, want) {
		t.Fatalf("calls = %#v, want %#v", target.calls, want)
	}
}

// TestWatchChangesDoesNotAddDependencies pins the ids-level gate: it arms the
// watch with the FULL id list (so truncating it fails) and, unlike OnChange,
// adds no dependency edges (ordering is the plan engine's job on the
// destination).
func TestWatchChangesDoesNotAddDependencies(t *testing.T) {
	target := &recorder{}
	WatchChanges("File[a]", "File[b]").Apply(target)
	want := []setterCall{{method: "SetChangeWatch", value: []string{"File[a]", "File[b]"}}}
	if !reflect.DeepEqual(target.calls, want) {
		t.Errorf("calls = %#v, want %#v", target.calls, want)
	}
}

// TestGuardsAreBuiltPerApplication checks Unless/OnlyIf build a fresh Guard
// each time the option is applied (two resources sharing one option value
// must not share a mutable Guard), default ExpectExit to 0, and apply
// GuardOptions in order so the last one wins.
func TestGuardsAreBuiltPerApplication(t *testing.T) {
	unless := Unless("test", []string{"-f", "/x"})
	first, second := &recorder{}, &recorder{}
	unless.Apply(first)
	unless.Apply(second)
	g1, g2 := first.calls[0].value.(*Guard), second.calls[0].value.(*Guard)
	if g1 == g2 {
		t.Fatal("Unless reused one *Guard across applications")
	}
	if want := (&Guard{Name: "test", Args: []string{"-f", "/x"}}); !reflect.DeepEqual(g1, want) {
		t.Errorf("guard = %+v, want %+v", g1, want)
	}

	target := &recorder{}
	OnlyIf("probe", nil, ExpectExit(1), ExpectStdout("a"), ExpectExit(3)).Apply(target)
	got := target.calls[0].value.(*Guard)
	if got.ExpectExit != 3 || got.ExpectStdout != "a" {
		t.Errorf("guard = %+v, want ExpectExit 3 (last wins) and ExpectStdout a", got)
	}
}

// TestLegacyAdaptersForwardErasedOptions checks every To*Options adapter
// keeps order and length and that each adapted option runs the original
// closure against the resource.
func TestLegacyAdaptersForwardErasedOptions(t *testing.T) {
	adapters := map[string]func(...Option) []interface{ Apply(any) }{
		"File":         adapt(ToFileOptions),
		"Dir":          adapt(ToDirOptions),
		"Link":         adapt(ToLinkOptions),
		"Package":      adapt(ToPackageOptions),
		"Service":      adapt(ToServiceOptions),
		"Cron":         adapt(ToCronOptions),
		"Timer":        adapt(ToTimerOptions),
		"SystemdTimer": adapt(ToSystemdTimerOptions),
		"DaemonReload": adapt(ToDaemonReloadOptions),
		"Command":      adapt(ToCommandOptions),
		"LocalUser":    adapt(ToLocalUserOptions),
	}
	for name, adapter := range adapters {
		t.Run(name, func(t *testing.T) {
			if got := adapter(); len(got) != 0 {
				t.Fatalf("no options adapted to %d", len(got))
			}
			target := &recorder{}
			for _, o := range adapter(DependsOn(fileA), IsAbsent) {
				o.Apply(target)
			}
			want := []setterCall{{method: "AddDependency", value: "File[a]"}, {method: "SetAbsent"}}
			if !reflect.DeepEqual(target.calls, want) {
				t.Errorf("calls = %#v, want %#v", target.calls, want)
			}
		})
	}
}

// adapt erases a typed To*Options adapter's element type so the adapters can
// share one table.
func adapt[T interface{ Apply(any) }](to func(...Option) []T) func(...Option) []interface{ Apply(any) } {
	return func(opts ...Option) []interface{ Apply(any) } {
		typed := to(opts...)
		out := make([]interface{ Apply(any) }, len(typed))
		for i, o := range typed {
			out[i] = o
		}
		return out
	}
}

// dependencyIDs returns the AddDependency values recorded on target.
func dependencyIDs(target *recorder) []string {
	var ids []string
	for _, call := range target.calls {
		if call.method == "AddDependency" {
			ids = append(ids, call.value.(string))
		}
	}
	return ids
}

// TestChangeGateFamilyLowersToOneSetter is the option-level equivalence
// test of the single change-gate family: every spelling reaches the one
// SetChangeWatch capability, the legacy IfChanged/WithWatch pair making
// exactly the calls of WatchChanges (after IfChanged's arm-only call), and
// OnChange adding only the ordering edges on top.
func TestChangeGateFamilyLowersToOneSetter(t *testing.T) {
	gateCalls := func(opts ...interface{ Apply(any) }) []setterCall {
		target := &recorder{}
		for _, o := range opts {
			o.Apply(target)
		}
		var gate []setterCall
		for _, c := range target.calls {
			if c.method == "SetChangeWatch" {
				gate = append(gate, c)
			}
		}
		return gate
	}
	want := gateCalls(WatchChanges("File[a]", "File[b]"))
	for name, got := range map[string][]setterCall{
		"WithWatch":             gateCalls(WithWatch("File[a]", "File[b]")),
		"OnChange":              gateCalls(OnChange(resource.Resource{Type: "File", Name: "a"}, resource.Resource{Type: "File", Name: "b"})),
		"IfChanged + WithWatch": gateCalls(IfChanged, WithWatch("File[a]", "File[b]"))[1:],
	} {
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s gate calls = %#v, want WatchChanges' %#v", name, got, want)
		}
	}
	if got := gateCalls(IfChanged); !reflect.DeepEqual(got, []setterCall{{method: "SetChangeWatch", value: []string(nil)}}) {
		t.Errorf("IfChanged gate calls = %#v, want one arm-only SetChangeWatch(nil)", got)
	}
}

// TestChangeGateCopiesIDs pins that a change-gate option records the ids
// it was built with, not whatever the caller's slice holds later.
func TestChangeGateCopiesIDs(t *testing.T) {
	ids := []string{"File[a]"}
	o := WatchChanges(ids...)
	ids[0] = "File[mutated]"
	target := &recorder{}
	o.Apply(target)
	if got := target.calls[0].value; !reflect.DeepEqual(got, []string{"File[a]"}) {
		t.Errorf("recorded %#v, want the ids at construction", got)
	}
}

// TestRecordedChangeGate pins the one apply-side rule every gated kind's
// plan handler shares: no option for an ungated op (its recorded watch ids
// are ignored), WatchChanges for a gated op, and an error naming the kind
// for a gated op without watch ids.
func TestRecordedChangeGate(t *testing.T) {
	if o, err := RecordedChangeGate("service", false, []string{"File[a]"}); o != nil || err != nil {
		t.Errorf("ungated = %v, %v, want nil, nil", o, err)
	}
	o, err := RecordedChangeGate("timer", true, []string{"File[a]"})
	if err != nil {
		t.Fatal(err)
	}
	target := &recorder{}
	o.Apply(target)
	want := []setterCall{{method: "SetChangeWatch", value: []string{"File[a]"}}}
	if !reflect.DeepEqual(target.calls, want) {
		t.Errorf("gated calls = %#v, want %#v", target.calls, want)
	}
	for _, kind := range []string{"command", "service", "timer", "daemon_reload"} {
		_, err := RecordedChangeGate(kind, true, nil)
		if err == nil || err.Error() != kind+": if_changed without watch ids" {
			t.Errorf("%s gated without watch = %v, want the if_changed refusal", kind, err)
		}
	}
}

// TestEmptyWithWatchIsANoOp pins WithWatch's one difference from
// WatchChanges: without ids it makes no setter call (the reload's gate is
// left as the other options set it, as before b72), where WatchChanges()
// aborts. It still requires opt.ChangeGated, so a resource without the
// DependsOn fallback keeps refusing it.
func TestEmptyWithWatchIsANoOp(t *testing.T) {
	target := &recorder{}
	WithWatch().Apply(target)
	if len(target.calls) != 0 {
		t.Errorf("WithWatch() calls = %#v, want none", target.calls)
	}
}
