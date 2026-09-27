package api

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/snonux/gonf/api/options"
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
		{"contains empty", func() TaskOption { return WhenHostnameContains("") }, `WhenHostnameContains: hostname fragment 1 ("") must not be empty or whitespace-only`},
		{"contains whitespace", func() TaskOption { return WhenHostnameContains(" \t") }, `WhenHostnameContains: hostname fragment 1 (" \t") must not be empty or whitespace-only`},
		{"in no hosts", func() TaskOption { return WhenHostnameIn() }, "WhenHostnameIn: no hosts"},
		{"in single empty", func() TaskOption { return WhenHostnameIn("") }, `WhenHostnameIn: hostname fragment 1 ("") must not be empty or whitespace-only`},
		{"in single whitespace", func() TaskOption { return WhenHostnameIn("  ") }, `WhenHostnameIn: hostname fragment 1 ("  ") must not be empty or whitespace-only`},
		{"in valid then empty", func() TaskOption { return WhenHostnameIn("f0", "") }, `WhenHostnameIn: hostname fragment 2 ("") must not be empty or whitespace-only`},
		{"in empty then valid", func() TaskOption { return WhenHostnameIn("", "f1") }, `WhenHostnameIn: hostname fragment 1 ("") must not be empty or whitespace-only`},
		{"in valid then whitespace", func() TaskOption { return WhenHostnameIn("f0", "f1", " ") }, `WhenHostnameIn: hostname fragment 3 (" ") must not be empty or whitespace-only`},
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

// blankHostGuard is a RegisterMethods recipe whose WhenBase companion
// narrows with a blank hostname fragment; blankHostGuardLine is the line
// of its runtime.Caller, so the WhenHostnameIn call is the line after.
type blankHostGuard struct{}

var blankHostGuardLine int

func (blankHostGuard) Base() {}

func (blankHostGuard) WhenBase() TaskOption {
	_, _, blankHostGuardLine, _ = runtime.Caller(0)
	return WhenHostnameIn("f0", "")
}

// TestHostnameGuardBlankInCompanionReportsCompanionLine: the refusal is
// reported where the guard is written (the WhenX companion line), not at
// the RegisterMethods call that applies it, and the method's task is still
// never activated or recorded.
func TestHostnameGuardBlankInCompanionReportsCompanionLine(t *testing.T) {
	requireDeclErr(t, `WhenHostnameIn: hostname fragment 2 ("") must not be empty or whitespace-only`, func() {
		RegisterMethods(blankHostGuard{}, WithPrefix("blank_"))
	})
	want := fmt.Sprintf("hostname_guard_test.go:%d", blankHostGuardLine+1)
	if loc := declerr.Location(declerr.First()); !strings.HasSuffix(loc, want) {
		t.Fatalf("declaration error location = %q, want the companion line (…%s)", loc, want)
	}

	if err := resource.ResetDeclarationError(); err == nil {
		t.Fatal("ResetDeclarationError returned nil, want the reported error")
	}
	if c, ok := findCandidate("blank_base"); !ok || len(c.planWhen) != 0 {
		t.Fatalf("blank_base candidate = %+v (queued %v), want queued with no plan guard", c, ok)
	}
	Activate(DetectFacts())
	for _, info := range Tasks() {
		if info.Name == "blank_base" {
			t.Fatal("a method whose companion guard was refused must never be activated")
		}
	}
	if _, err := RecordPlan("8b", "", "blank_base"); err == nil {
		t.Fatal("RecordPlan must refuse the method whose companion guard was refused")
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

// Task bc regression tests: the body-level WhenHostname used to document
// and honour "an empty substr always matches", so WhenHostname(cfg.Host,
// fn) with an unset value ran fn on every destination — the hazard task 8b
// closed for the option-level guards. It now shares their blank check.

// TestWhenHostnameRefusesBlankFragment: every blank spelling, alone or in
// a List next to entries that do match this host, is a declaration error
// at the recipe line, and no fragment of the call runs — not even the
// matching ones, so a half-blank list never half-applies.
func TestWhenHostnameRefusesBlankFragment(t *testing.T) {
	host, err := os.Hostname()
	if err != nil {
		t.Fatalf("os.Hostname: %v", err)
	}
	cases := []struct {
		name  string
		hosts []string // nil: the single-string form with blank
		blank string
		want  string
	}{
		{"string empty", nil, "", `WhenHostname: hostname fragment 1 ("") must not be empty or whitespace-only`},
		{"string whitespace", nil, " \t", `WhenHostname: hostname fragment 1 (" \t") must not be empty or whitespace-only`},
		{"list empty first", []string{"", host}, "", `WhenHostname: hostname fragment 1 ("") must not be empty or whitespace-only`},
		{"list whitespace last", []string{host, "f0", "  "}, "", `WhenHostname: hostname fragment 3 ("  ") must not be empty or whitespace-only`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ran := 0
			var line int
			requireDeclErrAt(t, tc.want, &line, func() {
				if tc.hosts == nil {
					line = thisLine() + 1
					WhenHostname(tc.blank, func() { ran++ })
					return
				}
				line = thisLine() + 1
				WhenHostname(List(tc.hosts...), func() { ran++ })
			})
			if ran != 0 {
				t.Fatalf("fn ran %d time(s) despite the refused fragment", ran)
			}
		})
	}
}

// TestWhenHostnameBlankFragmentFailsRecord: inside a recorded task body a
// blank fragment fails that record with the declaration error instead of
// recording a when_begin(hostname_contains "") block every destination
// would enter, and the body under it is never recorded.
func TestWhenHostnameBlankFragmentFailsRecord(t *testing.T) {
	dir := resetAliasTest(t)
	bodyRan := false
	Task("blank_body", "", func() {
		WhenHostname(List("f0", " "), func() {
			bodyRan = true
			File(filepath.Join(dir, "x"), options.WithContent("x"))
		})
	})

	ops, err := RecordPlan("bc", "", "blank_body")
	want := `WhenHostname: hostname fragment 2 (" ") must not be empty or whitespace-only`
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("RecordPlan = %v (ops %v), want the declaration error %q", err, opsKinds(ops), want)
	}
	if loc := declerr.Location(err); !strings.Contains(loc, "hostname_guard_test.go:") {
		t.Fatalf("error location = %q, want the WhenHostname line in this file", loc)
	}
	if bodyRan {
		t.Fatal("the body of a refused WhenHostname ran")
	}
}

// TestWhenHostnameValidFragmentsGuardOnDestination: non-blank fragments
// still record one hostname_contains block each and apply only where the
// destination's hostname contains the fragment (case insensitive).
func TestWhenHostnameValidFragmentsGuardOnDestination(t *testing.T) {
	dir := resetAliasTest(t)
	pair := filepath.Join(dir, "pair")
	rocky := filepath.Join(dir, "rocky")
	Task("body_guards", "", func() {
		WhenHostname(List("f0", "F1"), func() { File(pair, options.WithContent("p")) })
		WhenHostname("rocky", func() { File(rocky, options.WithContent("r")) })
	})

	ops, err := RecordPlan("bc", "", "body_guards")
	if err != nil {
		t.Fatal(err)
	}
	if err := declerr.First(); err != nil {
		t.Fatalf("valid WhenHostname fragments reported %v", err)
	}
	var guards []plan.Predicate
	for _, op := range ops {
		if op.Op == plan.KindWhenBegin {
			guards = append(guards, op.All...)
		}
	}
	want := []plan.Predicate{
		{Fact: "hostname_contains", Eq: "f0"},
		{Fact: "hostname_contains", Eq: "F1"},
		{Fact: "hostname_contains", Eq: "rocky"},
	}
	if !reflect.DeepEqual(guards, want) {
		t.Fatalf("recorded guards = %#v, want %#v", guards, want)
	}

	applyWithHostname(t, ops, "earth.lan")
	if exists(pair) || exists(rocky) {
		t.Fatalf("non-matching destination applied: pair=%v rocky=%v", exists(pair), exists(rocky))
	}
	applyWithHostname(t, ops, "f1.lan.example")
	if !exists(pair) || exists(rocky) {
		t.Fatalf("destination f1: pair written=%v (want true), rocky written=%v (want false)", exists(pair), exists(rocky))
	}
	applyWithHostname(t, ops, "ROCKY")
	if !exists(rocky) {
		t.Fatal("destination ROCKY did not apply the WhenHostname(rocky) fragment")
	}
}
