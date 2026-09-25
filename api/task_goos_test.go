package api

import (
	"reflect"
	"slices"
	"testing"

	"github.com/snonux/gonf/plan"
)

// goosGuards are the GOOS task guards and the one predicate each lowers to.
var goosGuards = []struct {
	name string
	opt  TaskOption
	want plan.Predicate
}{
	{"WhenLinux", WhenLinux(), plan.Predicate{Fact: "goos", Eq: "linux"}},
	{"WhenDarwin", WhenDarwin(), plan.Predicate{Fact: "goos", Eq: "darwin"}},
	{"WhenFreeBSD", WhenFreeBSD(), plan.Predicate{Fact: "goos", Eq: "freebsd"}},
	{"WhenOpenBSD", WhenOpenBSD(), plan.Predicate{Fact: "goos", Eq: "openbsd"}},
	{"WhenNetBSD", WhenNetBSD(), plan.Predicate{Fact: "goos", Eq: "netbsd"}},
	{"WhenBSD", WhenBSD(), plan.Predicate{Fact: "goos", In: []string{"freebsd", "openbsd", "netbsd"}}},
	{"WhenOS(linux,darwin)", WhenOS("linux", "darwin"), plan.Predicate{Fact: "goos", In: []string{"linux", "darwin"}}},
}

// TestGOOSGuardsRecordGoosPredicate pins each guard's serializable form: the
// predicate the task records as its when_begin.
func TestGOOSGuardsRecordGoosPredicate(t *testing.T) {
	for _, g := range goosGuards {
		var c taskCandidate
		g.opt(&c)
		if len(c.opaque) != 0 || !reflect.DeepEqual(c.planWhen, []plan.Predicate{g.want}) {
			t.Errorf("%s: planWhen %+v (opaque %d), want [%+v]", g.name, c.planWhen, len(c.opaque), g.want)
		}
	}
}

// TestGOOSGuardsEvaluateOnDestination: the recorded predicate matches
// exactly the intended GOOS set under the plan engine's own rules.
func TestGOOSGuardsEvaluateOnDestination(t *testing.T) {
	matches := map[string][]string{
		"WhenLinux":            {"linux"},
		"WhenDarwin":           {"darwin"},
		"WhenFreeBSD":          {"freebsd"},
		"WhenOpenBSD":          {"openbsd"},
		"WhenNetBSD":           {"netbsd"},
		"WhenBSD":              {"freebsd", "openbsd", "netbsd"},
		"WhenOS(linux,darwin)": {"linux", "darwin"},
	}
	for _, g := range goosGuards {
		for _, goos := range supportedGOOS {
			ok, err := plan.EvalPredicates([]plan.Predicate{g.want}, plan.Facts{GOOS: goos})
			if err != nil {
				t.Fatal(err)
			}
			if want := slices.Contains(matches[g.name], goos); ok != want {
				t.Errorf("%s on %s = %v, want %v", g.name, goos, ok, want)
			}
		}
	}
}

// TestGOOSGuardListMarking: -list keeps a task guarded for another OS and
// marks it destination-guarded; one whose GOOS set includes the host is
// unmarked. The recorded plan wraps it in the goos when_begin.
func TestGOOSGuardListMarking(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)
	Task("mac", "", func() {}, WhenDarwin())
	Task("unix", "", func() {}, WhenOS("linux", "darwin"))
	Activate(Facts{GOOS: "linux"})
	got := map[string]string{}
	for _, info := range Tasks() {
		got[info.Name] = info.DestinationGuard
	}
	if got["mac"] != "goos=darwin" || got["unix"] != "" {
		t.Fatalf("destination guards = %q, want mac=goos=darwin, unix unmarked", got)
	}
}

// TestGOOSGuardRecordsWhenBegin: the guard travels as the task's when_begin.
func TestGOOSGuardRecordsWhenBegin(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)
	Task("bsd", "", func() { File("/tmp/gonf-when-bsd-never-applied") }, WhenBSD())
	ops, err := RecordPlan("bsd", "", "bsd")
	if err != nil {
		t.Fatalf("RecordPlan: %v", err)
	}
	want := []plan.Predicate{{Fact: "goos", In: []string{"freebsd", "openbsd", "netbsd"}}}
	for _, op := range ops {
		if op.Op == plan.KindWhenBegin && reflect.DeepEqual(op.All, want) {
			return
		}
	}
	t.Fatalf("no when_begin %+v in %+v", want, ops)
}

// TestWhenOSRefusesUnknownGOOS: a name gonf does not manage, or no name, is
// a declaration error, and the task is never activated.
func TestWhenOSRefusesUnknownGOOS(t *testing.T) {
	for name, opt := range map[string]TaskOption{
		"unknown": WhenOS("linux", "windows"),
		"empty":   WhenOS(),
	} {
		t.Run(name, func(t *testing.T) {
			requireDeclErr(t, "WhenOS:", func() {
				Task("t", "", func() {}, opt)
			})
			if got := Matching("^t$"); len(got) != 0 {
				t.Fatalf("a refused WhenOS task must not activate: %v", got)
			}
		})
	}
}
