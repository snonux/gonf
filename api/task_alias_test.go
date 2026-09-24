package api

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/plan"
)

// resetAliasTest isolates one alias/aggregate test: a clean registry and
// recording session, no profile override, and a cleanup that also resets
// what the test left behind.
func resetAliasTest(t *testing.T) string {
	t.Helper()
	ResetForTest()
	t.Cleanup(ResetForTest)
	return t.TempDir()
}

// fileTask registers a task whose body records one File op for dir/name, so
// op order and counts identify which task bodies were recorded.
func fileTask(dir, name string, opts ...TaskOption) string {
	path := filepath.Join(dir, name)
	Task(name, "writes "+name, func() {
		File(path, options.WithContent(name))
	}, opts...)
	return path
}

// recordedFilePaths records names into a fresh plan and returns the paths of
// its file ops in plan order.
func recordedFilePaths(t *testing.T, names ...string) []string {
	t.Helper()
	ops, err := RecordPlan("alias-test", "", names...)
	if err != nil {
		t.Fatalf("RecordPlan(%v): %v", names, err)
	}
	return filePaths(ops)
}

func filePaths(ops []plan.Op) []string {
	var paths []string
	for _, op := range ops {
		if op.Op == plan.KindFile {
			paths = append(paths, op.Path)
		}
	}
	return paths
}

// recordErr records names and returns the (required) error.
func recordErr(t *testing.T, names ...string) error {
	t.Helper()
	_, err := RecordPlan("alias-test", "", names...)
	if err == nil {
		t.Fatalf("RecordPlan(%v): want an error, got nil", names)
	}
	return err
}

func wantErrContains(t *testing.T, err error, parts ...string) {
	t.Helper()
	for _, p := range parts {
		if !strings.Contains(err.Error(), p) {
			t.Errorf("error %q does not contain %q", err, p)
		}
	}
}

// TestAliasRecordsTargetOpsExactly pins that an alias records exactly the
// target's ops — including its serializable condition (when_begin/when_end)
// and its privilege chunk (elevate) — and nothing of its own.
func TestAliasRecordsTargetOpsExactly(t *testing.T) {
	dir := resetAliasTest(t)
	fileTask(dir, "target", Privileged(), WhenLinux())
	Alias("legacy", "Legacy alias for target", "target")

	viaTarget, err := RecordPlan("same", "", "target")
	if err != nil {
		t.Fatal(err)
	}
	viaAlias, err := RecordPlan("same", "", "legacy")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(viaTarget, viaAlias) {
		t.Fatalf("alias ops differ from target ops:\n target=%#v\n alias=%#v", viaTarget, viaAlias)
	}
	var sawWhen, sawElevatedFile bool
	for _, op := range viaAlias {
		sawWhen = sawWhen || (op.Op == plan.KindWhenBegin && op.ID == "when.target")
		sawElevatedFile = sawElevatedFile || (op.Op == plan.KindFile && op.Elevate)
	}
	if !sawWhen || !sawElevatedFile {
		t.Fatalf("alias lost the target's condition (%v) or privilege (%v): %#v", sawWhen, sawElevatedFile, viaAlias)
	}
}

// TestAliasListedWithTargetActivation pins the listing contract: an alias is
// a public name with its own description and AliasOf, active exactly when
// its target is (and destination-guarded exactly when it is), and it matches
// patterns like any task.
func TestAliasListedWithTargetActivation(t *testing.T) {
	dir := resetAliasTest(t)
	fileTask(dir, "home_agents")
	fileTask(dir, "fedora_only", When(ProfileIs("fedora")))
	fileTask(dir, "guarded", WhenProfile("fedora"))
	Alias("home_prompts", "Legacy alias for home_agents", "home_agents")
	Alias("fedora_alias", "", "fedora_only")
	Alias("guarded_alias", "", "guarded")

	SetProfileOverride("rocky")
	Activate(DetectFacts())
	got := map[string]TaskInfo{}
	for _, info := range Tasks() {
		got[info.Name] = info
	}
	want := TaskInfo{Name: "home_prompts", Description: "Legacy alias for home_agents", AliasOf: "home_agents"}
	if got["home_prompts"] != want {
		t.Fatalf("alias info = %+v, want %+v", got["home_prompts"], want)
	}
	if got["home_agents"].AliasOf != "" {
		t.Fatalf("a real task must have no AliasOf: %+v", got["home_agents"])
	}
	if _, ok := got["fedora_alias"]; ok {
		t.Fatal("alias of an inactive target must not be listed")
	}
	if g := got["guarded_alias"].DestinationGuard; g != "profile=fedora" {
		t.Fatalf("alias of a destination-guarded target: DestinationGuard = %q, want profile=fedora", g)
	}
	if m := Matching("^home_"); !reflect.DeepEqual(m, []string{"home_agents", "home_prompts"}) {
		t.Fatalf("Matching = %v", m)
	}

	SetProfileOverride("fedora")
	Activate(DetectFacts())
	if m := Matching("^fedora_"); !reflect.DeepEqual(m, []string{"fedora_alias", "fedora_only"}) {
		t.Fatalf("alias must activate with its target, Matching = %v", m)
	}
}

// TestAliasRegisteredBeforeTarget pins that registration order is free: the
// target is resolved when the plan is recorded.
func TestAliasRegisteredBeforeTarget(t *testing.T) {
	dir := resetAliasTest(t)
	Alias("early", "", "late")
	path := fileTask(dir, "late")

	if got := recordedFilePaths(t, "early"); !reflect.DeepEqual(got, []string{path}) {
		t.Fatalf("alias ops = %v, want %v", got, []string{path})
	}
}

// TestAliasUnknownTargetFails covers a typo'd target: the alias is not
// listed, and naming it fails the record with both names.
func TestAliasUnknownTargetFails(t *testing.T) {
	resetAliasTest(t)
	Task("real", "", func() {})
	Alias("broken", "", "no_such_task")

	for _, info := range Tasks() {
		if info.Name == "broken" {
			t.Fatal("an alias with an unknown target must not be listed")
		}
	}
	wantErrContains(t, recordErr(t, "broken"), `alias "broken" targets unknown task "no_such_task"`)
	if err := Run("broken"); err == nil {
		t.Fatal("Run of a broken alias must fail")
	}
}

// TestAliasOfAliasRefused pins the one-level rule: an alias naming another
// alias is neither listed nor recordable, and the error names the real task
// to point it at.
func TestAliasOfAliasRefused(t *testing.T) {
	dir := resetAliasTest(t)
	fileTask(dir, "real")
	Alias("first", "", "real")
	Alias("second", "", "first")

	for _, info := range Tasks() {
		if info.Name == "second" {
			t.Fatal("an alias of an alias must not be listed")
		}
	}
	wantErrContains(t, recordErr(t, "second"), `alias "second" targets alias "first"`, `"real"`)
}

// TestAliasCycleThroughAlias covers a target whose body runs its own alias:
// the cycle is detected and the chain names the alias.
func TestAliasCycleThroughAlias(t *testing.T) {
	resetAliasTest(t)
	Task("loop", "", func() { _ = Run("loop_alias") })
	Alias("loop_alias", "", "loop")

	wantErrContains(t, recordErr(t, "loop"), "task recursion cycle detected: loop -> loop_alias -> loop")
	wantErrContains(t, recordErr(t, "loop_alias"), "loop_alias -> loop -> loop_alias")
}

// TestAggregateRecordsAliasTargetOnce is the F8 regression: a pattern that
// matches both an alias and its target records the target once (a body-only
// Run wrapper used to record it twice).
func TestAggregateRecordsAliasTargetOnce(t *testing.T) {
	dir := resetAliasTest(t)
	agents := fileTask(dir, "home_agents")
	bash := fileTask(dir, "home_bash")
	Alias("home_prompts", "Legacy alias for home_agents", "home_agents")
	Aggregate("home", "all home", "^home_")

	got := recordedFilePaths(t, "home")
	if want := []string{agents, bash}; !reflect.DeepEqual(got, want) {
		t.Fatalf("home ops = %v, want %v", got, want)
	}
}

// TestAggregateAliasKeepsFirstPosition pins the dedupe order: an alias that
// sorts before its target records the target at the alias's position.
func TestAggregateAliasKeepsFirstPosition(t *testing.T) {
	dir := resetAliasTest(t)
	middle := fileTask(dir, "p_m")
	last := fileTask(dir, "p_z")
	Alias("p_a", "", "p_z")
	Aggregate("p", "", "^p_")

	if got, want := recordedFilePaths(t, "p"), []string{last, middle}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ops = %v, want %v", got, want)
	}
}

// TestAggregateBrokenAliasMember pins both aggregate forms against a broken
// alias: it is never active, so a pattern never matches it, but naming it
// as an explicit member fails the record instead of being skipped quietly.
func TestAggregateBrokenAliasMember(t *testing.T) {
	dir := resetAliasTest(t)
	realPath := fileTask(dir, "s_real")
	Alias("s_broken", "", "s_missing")
	Alias("s_chained", "", "s_broken")
	Aggregate("s", "", "^s_")
	AggregateTasks("setup", "", "s_real", "s_broken")
	AggregateTasks("setup_chained", "", "s_chained", "s_real")

	if got := recordedFilePaths(t, "s"); !reflect.DeepEqual(got, []string{realPath}) {
		t.Fatalf("pattern aggregate ops = %v, want only %v", got, realPath)
	}
	wantErrContains(t, recordErr(t, "setup"), `aggregate setup: alias "s_broken" targets unknown task "s_missing"`)
	wantErrContains(t, recordErr(t, "setup_chained"), `aggregate setup_chained: alias "s_chained" targets alias "s_broken"`)
}

// TestAggregateTasksDeclaredOrderAndDedupe pins explicit membership: members
// run in list order (not sorted), aliases resolve to their targets, and a
// target listed directly and through an alias records once.
func TestAggregateTasksDeclaredOrderAndDedupe(t *testing.T) {
	dir := resetAliasTest(t)
	b := fileTask(dir, "b")
	a := fileTask(dir, "a")
	c := fileTask(dir, "c")
	Alias("c_alias", "", "c")
	AggregateTasks("setup", "explicit setup", "c_alias", "b", "a", "c")

	if got, want := recordedFilePaths(t, "setup"), []string{c, b, a}; !reflect.DeepEqual(got, want) {
		t.Fatalf("setup ops = %v, want %v", got, want)
	}
}

// TestAggregateTasksSkipsInactiveMembers pins that explicit membership keeps
// the controller-side activation filter a pattern aggregate applies (opaque
// When predicates only, task 8h2), and that a list with no active member
// fails like an empty pattern match.
func TestAggregateTasksSkipsInactiveMembers(t *testing.T) {
	dir := resetAliasTest(t)
	always := fileTask(dir, "always")
	fileTask(dir, "fedora_only", When(ProfileIs("fedora")))
	AggregateTasks("setup", "", "fedora_only", "always")
	AggregateTasks("fedora_setup", "", "fedora_only")

	SetProfileOverride("rocky")
	Activate(DetectFacts())
	if got := recordedFilePaths(t, "setup"); !reflect.DeepEqual(got, []string{always}) {
		t.Fatalf("setup ops = %v, want %v", got, []string{always})
	}
	wantErrContains(t, recordErr(t, "fedora_setup"), "aggregate fedora_setup: no member task is active")
}

// TestAggregateTasksUnknownMemberFails pins that a typo'd member fails the
// record instead of silently shrinking the setup run.
func TestAggregateTasksUnknownMemberFails(t *testing.T) {
	dir := resetAliasTest(t)
	fileTask(dir, "real")
	AggregateTasks("setup", "", "real", "typo")

	wantErrContains(t, recordErr(t, "setup"), `aggregate setup: unknown member task "typo"`)
}

// TestOperationalNeverAutoIncluded pins the safety rule: a pattern aggregate
// skips operational tasks and aliases of them, an explicit AggregateTasks
// may still list one, and the task stays callable by name.
func TestOperationalNeverAutoIncluded(t *testing.T) {
	dir := resetAliasTest(t)
	setup := fileTask(dir, "fe_setup")
	invoke := fileTask(dir, "fe_invoke", Operational())
	Alias("fe_invoke_now", "", "fe_invoke")
	Aggregate("fe", "", "^fe_")
	Aggregate("ops_only", "", "^fe_invoke")
	AggregateTasks("with_invoke", "", "fe_setup", "fe_invoke")

	if got := recordedFilePaths(t, "fe"); !reflect.DeepEqual(got, []string{setup}) {
		t.Fatalf("pattern aggregate ops = %v, want only %v", got, setup)
	}
	wantErrContains(t, recordErr(t, "ops_only"), "matched no tasks", "operational")
	if got := recordedFilePaths(t, "with_invoke"); !reflect.DeepEqual(got, []string{setup, invoke}) {
		t.Fatalf("explicit aggregate ops = %v", got)
	}
	if got := recordedFilePaths(t, "fe_invoke_now"); !reflect.DeepEqual(got, []string{invoke}) {
		t.Fatalf("operational alias ops = %v", got)
	}
}

// TestNestedAggregateThroughAliasPropagatesError pins nested error
// propagation: explicit outer -> pattern inner -> alias -> failing target,
// with the whole aggregate chain and the real cause in the error.
func TestNestedAggregateThroughAliasPropagatesError(t *testing.T) {
	dir := resetAliasTest(t)
	Task("leaf", "", func() {
		File(filepath.Join(dir, "out"), options.WithSource(filepath.Join(dir, "does-not-exist.src")))
	})
	Alias("inner_leaf", "", "leaf")
	Aggregate("inner", "", "^inner_")
	AggregateTasks("outer", "", "inner")

	wantErrContains(t, recordErr(t, "outer"), "aggregate outer: aggregate inner:", "does-not-exist")
}

// TestNestedRunErrorFailsRecordEvenWhenIgnored pins that `_ = Run(...)` in a
// task body cannot swallow a child failure: the enclosing record fails and
// names the body that ran it.
func TestNestedRunErrorFailsRecordEvenWhenIgnored(t *testing.T) {
	dir := resetAliasTest(t)
	fileTask(dir, "fine")
	Task("wrapper", "", func() {
		_ = Run("fine")
		_ = Run("no_such_task")
	})

	wantErrContains(t, recordErr(t, "wrapper"), `task wrapper: unknown task "no_such_task"`)
	// The failure must not leak into the next, healthy session.
	if got := recordedFilePaths(t, "fine"); len(got) != 1 {
		t.Fatalf("healthy record after a failed one = %v", got)
	}
}

// TestNestedRunReturnsErrorToBody pins that a body still sees the nested
// error it can act on (the stash does not replace the return value).
func TestNestedRunReturnsErrorToBody(t *testing.T) {
	resetAliasTest(t)
	var seen error
	Task("wrapper", "", func() { seen = Run("missing") })

	_ = recordErr(t, "wrapper")
	if seen == nil || !strings.Contains(seen.Error(), `unknown task "missing"`) {
		t.Fatalf("nested Run returned %v to the body", seen)
	}
}

// TestTaskAliasRegistrationMisuseIsDeclarationError checks every
// registration-time misuse of Task, Alias and AggregateTasks in-process: it is
// reported as a declaration error with the expected message (no process
// exit), the offending name is not queued, and the first registration of a
// duplicated name keeps it.
func TestTaskAliasRegistrationMisuseIsDeclarationError(t *testing.T) {
	noop := func() {}
	cases := []struct {
		caseName, want string
		declare        func()
	}{
		{"alias-dup-task", `Task "x" already queued`, func() { Task("x", "", noop); Alias("x", "", "y") }},
		{"task-dup-alias", `Task "a" already queued`, func() { Alias("a", "", "x"); Task("a", "", noop) }},
		{"alias-self", `Alias "a": must not target itself`, func() { Alias("a", "", "a") }},
		{"alias-empty-target", `Alias "a": target must not be empty`, func() { Alias("a", "", "") }},
		{"alias-empty-name", "Alias: name must not be empty", func() { Alias("", "", "x") }},
		{"agg-no-members", `AggregateTasks "agg": at least one member task is required`, func() { AggregateTasks("agg", "") }},
		{"agg-dup-member", `AggregateTasks "agg": member "x" listed twice`, func() { AggregateTasks("agg", "", "x", "y", "x") }},
		{"agg-self-member", `AggregateTasks "agg": must not list itself`, func() { AggregateTasks("agg", "", "x", "agg") }},
		{"agg-empty-member", `AggregateTasks "agg": member name must not be empty`, func() { AggregateTasks("agg", "", "x", "") }},
		{"agg-dup-name", `Task "x" already queued`, func() { Task("x", "", noop); AggregateTasks("x", "", "y") }},
		{"agg-member-alias-of-self", `AggregateTasks "setup": member "setup_alias" is an alias of the aggregate itself`, func() {
			Alias("setup_alias", "", "setup")
			AggregateTasks("setup", "", "a", "setup_alias")
		}},
		{"alias-of-agg-listing-it", `Alias "setup_alias": aggregate "setup" lists it as a member`, func() {
			AggregateTasks("setup", "", "a", "setup_alias")
			Alias("setup_alias", "", "setup")
		}},
		{"task-empty-name", "Task: name must not be empty", func() { Task("", "", noop) }},
		{"task-nil-fn", `Task "x": fn must not be nil`, func() { Task("x", "", nil) }},
	}
	for _, tc := range cases {
		t.Run(tc.caseName, func(t *testing.T) {
			requireDeclErr(t, tc.want, tc.declare)
		})
	}
}

// TestDuplicateTaskKeepsFirstRegistration: a refused duplicate is not
// queued, so the first registration keeps the name and its body.
func TestDuplicateTaskKeepsFirstRegistration(t *testing.T) {
	var ran string
	requireDeclErr(t, `Task "x" already queued`, func() {
		Task("x", "first", func() { ran = "first" })
		Task("x", "second", func() { ran = "second" })
	})
	c, ok := findCandidate("x")
	if !ok || c.description != "first" {
		t.Fatalf("candidate x = %+v (found %t), want the first registration", c, ok)
	}
	c.fn()
	if ran != "first" {
		t.Fatalf("body = %q, want first", ran)
	}
}
