package api

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/plan"
)

// needsPaths maps task names to the file paths fileTask writes for them.
func needsPaths(dir string, names ...string) []string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = filepath.Join(dir, n)
	}
	return out
}

// requirePaths records names and compares the file ops, in plan order, with
// the files of the tasks want.
func requirePaths(t *testing.T, dir string, names []string, want ...string) {
	t.Helper()
	if got := recordedFilePaths(t, names...); !reflect.DeepEqual(got, needsPaths(dir, want...)) {
		t.Fatalf("Run%v records %v, want tasks %v", names, got, want)
	}
}

// TestNeedsRecordsNeededTasksFirst: a needed task records before its
// dependent, in declaration order, and a need's own needs come first.
func TestNeedsRecordsNeededTasksFirst(t *testing.T) {
	dir := resetAliasTest(t)
	fileTask(dir, "web", Needs("pf", "base"))
	fileTask(dir, "pf", Needs("net"))
	fileTask(dir, "base")
	fileTask(dir, "net")

	requirePaths(t, dir, []string{"web"}, "net", "pf", "base", "web")
}

// TestNeedsDedupesWithinOneRunList: a need the list already recorded is not
// recorded again, and a later explicit name a Needs already recorded is
// skipped; diamonds record the shared need once.
func TestNeedsDedupesWithinOneRunList(t *testing.T) {
	dir := resetAliasTest(t)
	fileTask(dir, "a", Needs("c"))
	fileTask(dir, "b", Needs("c"))
	fileTask(dir, "c")

	requirePaths(t, dir, []string{"c", "a"}, "c", "a")
	requirePaths(t, dir, []string{"a", "c"}, "c", "a")
	requirePaths(t, dir, []string{"a", "b"}, "c", "a", "b")
}

// TestNeedsLeavesPlansWithoutNeedsUnchanged pins that the Needs scope never
// deduplicates an explicit repeat: Run("x", "x") still records x twice.
func TestNeedsLeavesPlansWithoutNeedsUnchanged(t *testing.T) {
	dir := resetAliasTest(t)
	fileTask(dir, "x")
	fileTask(dir, "y", Needs("z"))
	fileTask(dir, "z")

	requirePaths(t, dir, []string{"x", "x"}, "x", "x")
	requirePaths(t, dir, []string{"z", "y", "z"}, "z", "y", "z")
}

// TestNeedsInAggregates: aggregate trees deduplicate needs with their
// members, whichever comes first.
func TestNeedsInAggregates(t *testing.T) {
	dir := resetAliasTest(t)
	fileTask(dir, "fe_web", Needs("fe_base"))
	fileTask(dir, "fe_base")
	AggregateTasks("explicit", "", "fe_web", "fe_base")
	Aggregate("pattern", "", "^fe_")

	requirePaths(t, dir, []string{"explicit"}, "fe_base", "fe_web")
	requirePaths(t, dir, []string{"pattern"}, "fe_base", "fe_web")
	requirePaths(t, dir, []string{"explicit", "fe_base"}, "fe_base", "fe_web")
}

// TestNeedsRecordOutsideTheDependentsGuard: the needed task records with its
// own (here: no) guard, before the dependent's when_begin, so Needs never
// narrows where it applies.
func TestNeedsRecordOutsideTheDependentsGuard(t *testing.T) {
	dir := resetAliasTest(t)
	fileTask(dir, "web", WhenLinux(), Privileged(), Needs("base"))
	fileTask(dir, "base")

	ops, err := RecordPlan("needs", "", "web")
	if err != nil {
		t.Fatalf("RecordPlan: %v", err)
	}
	ops = ops[1:] // drop the plan header
	if len(ops) < 2 || ops[0].Op != plan.KindFile || ops[0].Elevate || ops[1].Op != plan.KindWhenBegin {
		t.Fatalf("want base's unguarded, unelevated file op before web's when_begin, got %#v", ops)
	}
}

// TestNeedsResolvesRelativeToRegisterPrefix: in RegisterMethods a name is
// tried with the call's prefix first, then as a full name.
func TestNeedsResolvesRelativeToRegisterPrefix(t *testing.T) {
	dir := resetAliasTest(t)
	fileTask(dir, "global")
	needsDir = dir
	RegisterMethods(needsRecipe{}, WithPrefix("fe_"))

	requirePaths(t, dir, []string{"fe_web"}, "fe_base", "global", "fe_web")
}

// needsDir is where needsRecipe's bodies write (set by the test).
var needsDir string

type needsRecipe struct{}

func (needsRecipe) Base() { File(filepath.Join(needsDir, "fe_base"), options.WithContent("b")) }
func (needsRecipe) Web()  { File(filepath.Join(needsDir, "fe_web"), options.WithContent("w")) }

// OptsWeb needs Base relatively and "global" by its full name.
func (needsRecipe) OptsWeb() TaskOptions { return TaskOptions{Needs("base", "global")} }

// TestNeedsRefusals covers the negative cases: an unknown need fails the
// record; an empty name, a self need and a cycle are declaration errors
// that keep the task out of the registry.
func TestNeedsRefusals(t *testing.T) {
	dir := resetAliasTest(t)
	fileTask(dir, "web", Needs("missing"))
	wantErrContains(t, recordErr(t, "web"), `task "web" needs unknown task "missing"`)

	requireDeclErr(t, `Task "a": Needs: task name must not be empty`, func() {
		Task("a", "", func() {}, Needs(""))
	})
	requireDeclErr(t, `Task "a": Needs: a task must not need itself`, func() {
		Task("a", "", func() {}, Needs("a"))
	})
	requireDeclErr(t, `Task "c": Needs cycle: c -> a -> b -> c`, func() {
		Task("a", "", func() {}, Needs("b"))
		Task("b", "", func() {}, Needs("c"))
		Task("c", "", func() {}, Needs("a"))
	})
	if _, ok := findCandidate("c"); ok {
		t.Fatal("the task closing a Needs cycle must not be queued")
	}
	requireDeclErr(t, `Task "a": Needs cycle: a -> via -> b -> a`, func() {
		Task("b", "", func() {}, Needs("a"))
		Alias("via", "", "b")
		Task("a", "", func() {}, Needs("via"))
	})
}

// TestNeedsOperationalKeepsDependentOutOfPatterns: a task that needs
// operational work counts as operational for pattern aggregates.
func TestNeedsOperationalKeepsDependentOutOfPatterns(t *testing.T) {
	dir := resetAliasTest(t)
	fileTask(dir, "fe_invoke", Operational())
	fileTask(dir, "fe_web", Needs("fe_invoke"))
	fileTask(dir, "fe_base")
	Aggregate("fe", "", "^fe_")

	requirePaths(t, dir, []string{"fe"}, "fe_base")
	requirePaths(t, dir, []string{"fe_web"}, "fe_invoke", "fe_web")
}

// TestNeedsNestedRunIsItsOwnScope: a task body's Run(...) may carry its own
// guard, so a need recorded outside it is recorded again inside it.
func TestNeedsNestedRunIsItsOwnScope(t *testing.T) {
	dir := resetAliasTest(t)
	fileTask(dir, "web", Needs("base"))
	fileTask(dir, "base")
	Task("guarded", "", func() { _ = Run("web") }, WhenLinux())

	requirePaths(t, dir, []string{"base", "guarded"}, "base", "base", "web")
}
