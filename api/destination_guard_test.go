package api

import (
	"os"
	"reflect"
	"testing"

	"github.com/snonux/gonf/internal/privilege"
	"github.com/snonux/gonf/plan"
)

// Task 8h2 regression tests: serializable task guards travel to the
// destination instead of filtering aggregate membership on the controller.

// noMatchHost is a hostname substring no test machine carries, standing in
// for a destination such as rocky pushed to from a controller such as earth.
const noMatchHost = "no-such-host-8h2"

// whenBlocks returns, per when_begin op of ops, its ID and the paths of the
// file ops it wraps, so a test can assert which member sits under which
// guard.
func whenBlocks(ops []plan.Op) map[string][]string {
	out := map[string][]string{}
	var open []string
	for _, op := range ops {
		switch op.Op {
		case plan.KindWhenBegin:
			open = append(open, op.ID)
			out[op.ID] = nil
		case plan.KindWhenEnd:
			open = open[:len(open)-1]
		case plan.KindFile:
			if len(open) > 0 {
				id := open[len(open)-1]
				out[id] = append(out[id], op.Path)
			}
		}
	}
	return out
}

// applyWithHostname applies ops as a destination with that hostname would.
func applyWithHostname(t *testing.T, ops []plan.Op, hostname string) {
	t.Helper()
	facts := toPlanFacts(DetectFacts())
	facts.Hostname = hostname
	if err := plan.Apply(ops, facts, ""); err != nil {
		t.Fatalf("apply as %q: %v", hostname, err)
	}
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// TestPatternAggregateRecordsDestinationGuardedMember is the push-from-a-
// different-host case (dotfiles' home_tmux_rocky, pushed from earth): the
// controller's hostname does not match the member's guard, the destination's
// does. The pattern aggregate still records the member, inside its
// when_begin, and the destination applies it; a non-matching destination
// skips it.
func TestPatternAggregateRecordsDestinationGuardedMember(t *testing.T) {
	dir := resetAliasTest(t)
	base := fileTask(dir, "home_base")
	guarded := fileTask(dir, "home_tmux_rocky", WhenHostnameContains(noMatchHost))
	Aggregate("home", "", "^home_")

	ops, err := RecordPlan("push", "", "home")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := filePaths(ops), []string{base, guarded}; !reflect.DeepEqual(got, want) {
		t.Fatalf("home ops = %v, want %v", got, want)
	}
	if got := whenBlocks(ops)["when.home_tmux_rocky"]; !reflect.DeepEqual(got, []string{guarded}) {
		t.Fatalf("guarded member must sit inside its own when_begin, blocks = %v", whenBlocks(ops))
	}

	applyWithHostname(t, ops, "earth")
	if !exists(base) || exists(guarded) {
		t.Fatalf("non-matching destination: base written=%v (want true), guarded written=%v (want false)",
			exists(base), exists(guarded))
	}
	applyWithHostname(t, ops, "the-"+noMatchHost)
	if !exists(guarded) {
		t.Fatal("matching destination did not apply the destination-guarded member")
	}
}

// TestGroupWhenProfileMemberPushedFromOtherProfile is the WithGroupWhen
// (WhenProfile) group case: a controller of another profile records the
// group's tasks through a pattern aggregate, guarded by the profile.
func TestGroupWhenProfileMemberPushedFromOtherProfile(t *testing.T) {
	resetAliasTest(t)
	RegisterMethods(reflectPkg{}, WithPrefix("pkg_"), WithGroupWhen(WhenProfile("fedora")))
	Aggregate("pkgs", "", "^pkg_")

	SetProfileOverride("rocky")
	Activate(DetectFacts())
	ops, err := RecordPlan("push", "", "pkgs")
	if err != nil {
		t.Fatalf("a controller of another profile must record the group member: %v", err)
	}
	var guard []plan.Predicate
	for _, op := range ops {
		if op.Op == plan.KindWhenBegin && op.ID == "when.pkg_fedora" {
			guard = op.All
		}
	}
	if want := []plan.Predicate{{Fact: "profile", Eq: "fedora"}}; !reflect.DeepEqual(guard, want) {
		t.Fatalf("pkg_fedora guard = %#v, want %#v (ops %v)", guard, want, opsKinds(ops))
	}
}

// TestDestinationGuardedThroughAliasAndNestedAggregate pins points 3 and 4:
// a destination-guarded member reached through an Alias inside a nested
// aggregate records exactly like the member named explicitly — once, with
// its own when_begin.
func TestDestinationGuardedThroughAliasAndNestedAggregate(t *testing.T) {
	dir := resetAliasTest(t)
	guarded := fileTask(dir, "tmux_rocky", WhenHostnameContains(noMatchHost))
	Alias("o_tmux_legacy", "", "tmux_rocky")
	AggregateTasks("o_inner", "", "o_tmux_legacy", "tmux_rocky")
	Aggregate("outer", "", "^o_")

	explicit, err := RecordPlan("x", "", "tmux_rocky")
	if err != nil {
		t.Fatal(err)
	}
	nested, err := RecordPlan("x", "", "outer")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(explicit, nested) {
		t.Fatalf("nested/alias ops differ from the explicit ones:\n explicit=%v\n nested=%v",
			opsKinds(explicit), opsKinds(nested))
	}
	if got := whenBlocks(nested)["when.tmux_rocky"]; !reflect.DeepEqual(got, []string{guarded}) {
		t.Fatalf("blocks = %v", whenBlocks(nested))
	}
}

// TestMixedGuardsOpaqueFiltersSerializableTravels pins point 1: the opaque
// half of a mixed task filters on the controller, the serializable half
// travels.
func TestMixedGuardsOpaqueFiltersSerializableTravels(t *testing.T) {
	dir := resetAliasTest(t)
	kept := fileTask(dir, "m_kept", WhenHostnameContains(noMatchHost), When(func(Facts) bool { return true }))
	fileTask(dir, "m_dropped", WhenHostnameContains(noMatchHost), When(func(Facts) bool { return false }))
	Aggregate("mixed", "", "^m_")

	if got := Matching("^m_"); !reflect.DeepEqual(got, []string{"m_kept"}) {
		t.Fatalf("Matching = %v: only the opaque predicate may hide a task", got)
	}
	ops, err := RecordPlan("x", "", "mixed")
	if err != nil {
		t.Fatal(err)
	}
	if got := whenBlocks(ops)["when.m_kept"]; !reflect.DeepEqual(got, []string{kept}) {
		t.Fatalf("mixed member must record under its serializable guard, blocks = %v", whenBlocks(ops))
	}
	wantErrContains(t, recordErr(t, "m_dropped"), "fail on controller")
}

// TestAggregateTasksRecordsDestinationGuardedMember pins that an explicit
// member list follows the same rule as a pattern: a serializable guard does
// not drop the member.
func TestAggregateTasksRecordsDestinationGuardedMember(t *testing.T) {
	dir := resetAliasTest(t)
	always := fileTask(dir, "always")
	guarded := fileTask(dir, "guarded", WhenHostnameContains(noMatchHost))
	AggregateTasks("setup", "", "guarded", "always")
	AggregateTasks("guarded_only", "", "guarded")

	if got := recordedFilePaths(t, "setup"); !reflect.DeepEqual(got, []string{guarded, always}) {
		t.Fatalf("setup ops = %v", got)
	}
	if got := recordedFilePaths(t, "guarded_only"); !reflect.DeepEqual(got, []string{guarded}) {
		t.Fatalf("an all-destination-guarded list must record, got %v", got)
	}
}

// TestLocalRunSkipsMembersGuardedAwayFromThisHost pins point 2, local apply
// parity: a local Run's destination is this host, so an aggregate resolves
// its members' guards at record time exactly as before task 8h2. A
// Privileged member that cannot apply here must not split off an elevated
// chunk (no sudo/doas re-exec), whether it is reached by pattern, by an
// explicit list or through an alias; a member whose guard holds here still
// applies under its when_begin.
func TestLocalRunSkipsMembersGuardedAwayFromThisHost(t *testing.T) {
	host := DetectFacts().Hostname
	if host == "" {
		t.Skip("needs a hostname to build a matching guard")
	}
	calls := fakeElevation(t, privilege.Sudo, func([]plan.Op, string) error { return nil })
	dir := t.TempDir()
	base := fileTask(dir, "home_base")
	here := fileTask(dir, "home_here", WhenHostnameContains(host))
	away := fileTask(dir, "home_away", WhenHostnameContains(noMatchHost), Privileged())
	Alias("home_away_legacy", "", "home_away")
	Aggregate("home", "", "^home_")
	AggregateTasks("listed", "", "home_away_legacy", "home_base")
	AggregateTasks("away_only", "", "home_away")

	for _, name := range []string{"home", "listed"} {
		if err := Run(name); err != nil {
			t.Fatalf("Run(%s): %v", name, err)
		}
	}
	if len(*calls) != 0 {
		t.Fatalf("a member guarded away from this host re-executed elevated: %+v", *calls)
	}
	if !exists(base) || !exists(here) || exists(away) {
		t.Fatalf("written: base=%v here=%v away=%v, want true true false", exists(base), exists(here), exists(away))
	}
	if err := Run("away_only"); err == nil {
		t.Fatal("a local Run of a list whose only member cannot apply here must fail as before")
	}
}

// TestLocalDestinationRestored pins that setLocalDestination is scoped: a
// recording after a local Run records guarded members again.
func TestLocalDestinationRestored(t *testing.T) {
	dir := resetAliasTest(t)
	guarded := fileTask(dir, "guarded", WhenHostnameContains(noMatchHost))
	AggregateTasks("setup", "", "guarded", "plain")
	fileTask(dir, "plain")
	if err := Run("setup"); err != nil {
		t.Fatal(err)
	}
	if got := recordedFilePaths(t, "setup"); len(got) != 2 || got[0] != guarded {
		t.Fatalf("after a local Run, a plain record lost the guarded member: %v", got)
	}
}

// TestFormatGuard pins the -list rendering of a destination guard.
func TestFormatGuard(t *testing.T) {
	got := formatGuard([]plan.Predicate{
		{Fact: "goos", Eq: "linux"},
		{Fact: "profile", In: []string{"fedora", "rocky"}},
		{Fact: "hostname_contains", Eq: "rocky"},
	})
	if want := "goos=linux && profile=fedora|rocky && hostname_contains=rocky"; got != want {
		t.Fatalf("formatGuard = %q, want %q", got, want)
	}
	if got := unmetGuard(nil, Facts{}); got != "" {
		t.Fatalf("no guard must render empty, got %q", got)
	}
}

// TestWhenProfileWithoutProfilesIsOpaqueNever pins the degenerate
// WhenProfile(): it can match nothing and has no serializable form, so it is
// an opaque predicate that never holds — never activated, and naming it
// fails the record instead of applying the task unguarded.
func TestWhenProfileWithoutProfilesIsOpaqueNever(t *testing.T) {
	dir := resetAliasTest(t)
	fileTask(dir, "nobody", WhenProfile())
	if got := Matching("^nobody$"); len(got) != 0 {
		t.Fatalf("WhenProfile() must never activate: %v", got)
	}
	wantErrContains(t, recordErr(t, "nobody"), "fail on controller")
}
