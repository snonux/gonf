package api

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/internal/testutil"
	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
)

func TestTaskMatchingAndList(t *testing.T) {
	ResetTasks()
	resource.ResetRepository()

	Task("home_scripts", "Install scripts", func() {})
	Task("home_ssh", "Install ssh", func() {})
	Task("pkg_fedora", "Fedora packages", func() {})
	Task("home", "All home", func() {})

	got := Matching("^home_")
	want := []string{"home_scripts", "home_ssh"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Matching = %v, want %v", got, want)
	}

	infos := Tasks()
	if len(infos) != 4 {
		t.Fatalf("Tasks len = %d, want 4", len(infos))
	}
	if infos[0].Name != "home" || infos[0].Description != "All home" {
		t.Fatalf("first task = %+v", infos[0])
	}
}

func TestRunUnknownTask(t *testing.T) {
	ResetTasks()
	err := Run("nope")
	if err == nil {
		t.Fatal("expected error for unknown task")
	}
}

func TestRunNoTasks(t *testing.T) {
	ResetTasks()
	err := Run()
	if err == nil {
		t.Fatal("expected error for empty Run")
	}
}

func TestRunIsolatesRepositories(t *testing.T) {
	ResetTasks()
	dir := t.TempDir()
	aPath := filepath.Join(dir, "a.txt")
	bPath := filepath.Join(dir, "b.txt")

	Task("task_a", "writes a", func() {
		File(aPath, options.WithContent("a"))
	})
	Task("task_b", "writes b", func() {
		File(bPath, options.WithContent("b"))
	})

	if err := Run("task_a"); err != nil {
		t.Fatalf("Run task_a: %v", err)
	}
	if _, err := os.Stat(aPath); err != nil {
		t.Fatalf("a.txt missing: %v", err)
	}
	if _, err := os.Stat(bPath); !os.IsNotExist(err) {
		t.Fatal("b.txt should not exist after task_a alone")
	}

	if err := Run("task_b"); err != nil {
		t.Fatalf("Run task_b: %v", err)
	}
	if _, err := os.Stat(bPath); err != nil {
		t.Fatalf("b.txt missing: %v", err)
	}
}

func TestRunAggregateMatching(t *testing.T) {
	ResetTasks()
	dir := t.TempDir()
	aPath := filepath.Join(dir, "a.txt")
	bPath := filepath.Join(dir, "b.txt")

	Task("demo_a", "", func() {
		File(aPath, options.WithContent("a"))
	})
	Task("demo_b", "", func() {
		File(bPath, options.WithContent("b"))
	})
	Task("demo", "all demo_*", func() {
		if err := Run(Matching("^demo_")...); err != nil {
			panic(err)
		}
	})

	if err := Run("demo"); err != nil {
		t.Fatalf("Run demo: %v", err)
	}
	for _, p := range []string{aPath, bPath} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("%s missing: %v", p, err)
		}
	}
}

// TestRunAggregateSelfMatchingPattern covers an Aggregate whose pattern
// matches its own name (e.g. ".*"): the own-name exclusion must keep Run from
// recursing into itself, and the child must be recorded and applied once.
func TestRunAggregateSelfMatchingPattern(t *testing.T) {
	ResetTasks()
	dir := t.TempDir()
	childPath := filepath.Join(dir, "child.txt")

	Task("all_child", "", func() {
		File(childPath, options.WithContent("child"))
	})
	Aggregate("all", "", ".*")

	if err := Run("all"); err != nil {
		t.Fatalf("Run all: %v", err)
	}
	data, err := os.ReadFile(childPath)
	if err != nil {
		t.Fatalf("child file missing: %v", err)
	}
	if string(data) != "child" {
		t.Fatalf("child content = %q", data)
	}

	// The aggregate must expand to its child exactly once: exactly one file
	// op for the child path and none for the aggregate itself.
	ops, err := RecordPlan("count", testutil.PrivateTempDir(t), "all")
	if err != nil {
		t.Fatalf("RecordPlan: %v", err)
	}
	count := 0
	for _, op := range ops {
		if op.Op == plan.KindFile && op.Path == childPath {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("child file ops = %d, want 1", count)
	}
}

// TestRunTaskSelfRecursionCycle covers a plain task whose body Runs itself:
// the recorder must fail the record with a cycle error instead of crashing.
func TestRunTaskSelfRecursionCycle(t *testing.T) {
	ResetTasks()
	Task("self", "runs itself", func() {
		_ = Run("self") // cycle error cannot be returned from a task body
	})

	err := Run("self")
	if err == nil {
		t.Fatal("expected recursion cycle error, got nil")
	}
	if !strings.Contains(err.Error(), "self -> self") {
		t.Fatalf("error %v does not name the cycle", err)
	}
}

// TestRunMutualRecursionCycle covers a -> b -> a: the error must name both
// tasks and the cycle chain.
func TestRunMutualRecursionCycle(t *testing.T) {
	ResetTasks()
	Task("cyc_a", "", func() {
		_ = Run("cyc_b")
	})
	Task("cyc_b", "", func() {
		_ = Run("cyc_a")
	})

	err := Run("cyc_a")
	if err == nil {
		t.Fatal("expected recursion cycle error, got nil")
	}
	if !strings.Contains(err.Error(), "cyc_a -> cyc_b -> cyc_a") {
		t.Fatalf("error %v does not name the cycle", err)
	}
}

// TestRunDeepRecursionCycle covers a -> b -> c -> a.
func TestRunDeepRecursionCycle(t *testing.T) {
	ResetTasks()
	Task("deep_a", "", func() { _ = Run("deep_b") })
	Task("deep_b", "", func() { _ = Run("deep_c") })
	Task("deep_c", "", func() { _ = Run("deep_a") })

	err := Run("deep_a")
	if err == nil {
		t.Fatal("expected recursion cycle error, got nil")
	}
	if !strings.Contains(err.Error(), "deep_a -> deep_b -> deep_c -> deep_a") {
		t.Fatalf("error %v does not name the cycle", err)
	}
}

// TestRunDiamondIncludesNoCycle covers legal repeats: the same shared task
// included by two sibling branches must not be flagged as a cycle. Current
// behavior records the shared task once per including branch (the duplicate
// ops apply idempotently); only true cycles are errors.
func TestRunDiamondIncludesNoCycle(t *testing.T) {
	ResetTasks()
	dir := t.TempDir()
	sharedPath := filepath.Join(dir, "shared.txt")

	Task("dia_c", "", func() {
		File(sharedPath, options.WithContent("shared"))
	})
	Task("dia_a", "", func() {
		_ = Run("dia_c")
	})
	Task("dia_b", "", func() {
		_ = Run("dia_c")
	})
	Task("dia_root", "", func() {
		_ = Run("dia_a", "dia_b")
	})

	if err := Run("dia_root"); err != nil {
		t.Fatalf("Run dia_root: %v", err)
	}
	data, err := os.ReadFile(sharedPath)
	if err != nil {
		t.Fatalf("shared file missing: %v", err)
	}
	if string(data) != "shared" {
		t.Fatalf("shared content = %q", data)
	}

	// Preserve current behavior: the shared task is recorded once per
	// including branch, so two identical file ops reach the plan.
	ops, err := RecordPlan("count", testutil.PrivateTempDir(t), "dia_root")
	if err != nil {
		t.Fatalf("RecordPlan: %v", err)
	}
	count := 0
	for _, op := range ops {
		if op.Op == plan.KindFile && op.Path == sharedPath {
			count++
		}
	}
	if count != 2 {
		t.Fatalf("shared file ops = %d, want 2 (diamond repeats stay legal)", count)
	}
}

// TestRunAggregateSelfExclusionSugar covers the aggregate own-name exclusion:
// a self-matching pattern must not reach the cycle detector at all, and the
// child must still run.
func TestRunAggregateSelfExclusionSugar(t *testing.T) {
	ResetTasks()
	dir := t.TempDir()
	childPath := filepath.Join(dir, "demo_child.txt")

	Task("demo_child", "", func() {
		File(childPath, options.WithContent("demo-child"))
	})
	Aggregate("demo", "", "^demo")

	if err := Run("demo"); err != nil {
		t.Fatalf("Run demo: %v", err)
	}
	data, err := os.ReadFile(childPath)
	if err != nil {
		t.Fatalf("demo_child file missing: %v", err)
	}
	if string(data) != "demo-child" {
		t.Fatalf("demo_child content = %q", data)
	}
}

func TestRunUsesPlanApplyEngine(t *testing.T) {
	ResetTasks()
	dir := t.TempDir()
	path := filepath.Join(dir, "via-plan.txt")

	Task("via_plan", "", func() {
		File(path, options.WithContent("from-plan-engine"))
	}, WhenLinux())

	if err := Run("via_plan"); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "linux" {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatal("non-linux should skip WhenLinux task body via when_begin")
		}
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "from-plan-engine" {
		t.Fatalf("got %q", data)
	}
}

// TestRunFileTemplateSourceRendersOnDestination pins the g5 fix: a File
// resource whose WithSource carries a ".tmpl" suffix must be rendered as a
// text/template by the plan-record/apply path Run always goes through
// (api.Run -> RecordPlan -> plan.Apply, same engine push/cluster/fleet use),
// not written as raw template text. Before the fix, planDraft recorded the
// already-".tmpl"-stripped destination Path plus the raw source bytes with
// no signal that templating was intended, so plan.Apply's applyFile built a
// File with WithContent(rawBytes) and neither its path nor its (empty)
// source ended in ".tmpl" — shouldRenderTemplate stayed false and the
// literal "{{.Param}}" text landed on disk.
func TestRunFileTemplateSourceRendersOnDestination(t *testing.T) {
	ResetTasks()
	resource.ResetRepository()
	t.Cleanup(func() {
		resource.SetPlanDraftRecorder(nil)
		plan.SetRecording(false)
		plan.ResetRecord()
	})

	dir := t.TempDir()
	src := filepath.Join(dir, "app.conf.tmpl")
	if err := os.WriteFile(src, []byte("value={{.Param}}\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "app.conf")

	Task("file_tmpl_via_run", "", func() {
		File(dst, options.WithSource(src))
	})

	if err := Run("file_tmpl_via_run"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	data, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read rendered file: %v", err)
	}
	want := "value=" + src + "\n"
	if string(data) != want {
		t.Fatalf("rendered content = %q, want %q (raw template text means the plan-record/apply path never rendered it)", data, want)
	}
}

// TestNestedRunSurfacesRealPackError pins that a packaging failure (here: a
// source file that cannot be read) inside a nested Run fails the OUTER body
// with the REAL error, not a misleading "registered resources without plan
// draft" secondary one. Before the recordingPackErr sharing, nested Run kept
// its own packErr that the active session's recorder never wrote to, so
// aggregates died with the wrong message (task n12 review).
func TestNestedRunSurfacesRealPackError(t *testing.T) {
	ResetTasks()
	resource.ResetRepository()
	t.Cleanup(func() {
		resource.SetPlanDraftRecorder(nil)
		plan.SetRecording(false)
		plan.ResetRecord()
	})

	dir := t.TempDir()
	out := filepath.Join(dir, "out.conf")

	Task("child_bad_source", "child with unreadable source", func() {
		File(out, options.WithSource(filepath.Join(dir, "does-not-exist.src")))
	})
	Task("outer_nested", "nested Run over the bad child", func() {
		_ = Run("child_bad_source")
	})

	_, err := RecordPlan("nested_pack_err", testutil.PrivateTempDir(t), "outer_nested")
	if err == nil {
		t.Fatal("expected the record to fail on the unreadable source")
	}
	if !strings.Contains(err.Error(), "does-not-exist") {
		t.Errorf("error should surface the REAL packaging failure, got: %v", err)
	}
	if strings.Contains(err.Error(), "without plan draft") {
		t.Errorf("the misleading secondary error leaked: %v", err)
	}
}

// TestAggregateNoMatchFailsRecord covers a misconfigured Aggregate whose
// pattern matches no tasks: the body cannot return errors, so the failure is
// stashed and fails the record with a returned error naming the aggregate —
// no process exit, so Run's deferred temp-dir cleanup still runs.
func TestAggregateNoMatchFailsRecord(t *testing.T) {
	ResetTasks()
	resource.ResetRepository()
	t.Cleanup(func() {
		plan.SetRecording(false)
		plan.ResetRecord()
	})

	Task("unrelated", "", func() {})
	Aggregate("agg", "", "^no_such_task_")

	err := Run("agg")
	if err == nil {
		t.Fatal("expected the record to fail when the pattern matches no tasks")
	}
	if !strings.Contains(err.Error(), "aggregate agg: pattern") {
		t.Errorf("error should name the aggregate, got: %v", err)
	}
	if !strings.Contains(err.Error(), "matched no tasks") {
		t.Errorf("error should name the empty match, got: %v", err)
	}
}

// TestAggregateChildFailureFailsRecord covers a child task failing to record
// inside an Aggregate: the body stashes the error (it cannot return it), the
// enclosing record fails with a returned error naming the aggregate, and
// nothing is applied (the abort happens before plan apply runs).
func TestAggregateChildFailureFailsRecord(t *testing.T) {
	ResetTasks()
	resource.ResetRepository()
	t.Cleanup(func() {
		plan.SetRecording(false)
		plan.ResetRecord()
	})

	// The child's record fails: it registers a resource but no plan draft,
	// which checkUnrecordedDrafts rejects at record time.
	Task("child_no_draft", "", func() {
		resource.Register("Test", "no-draft",
			resource.ApplierFunc(func() error { return nil }))
	})
	Aggregate("agg", "runs the child", "^child_")

	err := Run("agg")
	if err == nil {
		t.Fatal("expected the aggregate's record to fail with the child's error")
	}
	if !strings.Contains(err.Error(), `aggregate agg: RecordPlan: task "child_no_draft": registered resources without plan draft`) {
		t.Errorf("error should name the aggregate and the child cause, got: %v", err)
	}
}

// TestNestedAggregateChainVisibleInError pins that nested aggregate failures
// keep the include chain in the error: A includes B, B's child fails, and the
// top-level error must mention both aggregates (wrap-the-existing-stash in
// stashBodyError), not only the innermost one.
func TestNestedAggregateChainVisibleInBodyError(t *testing.T) {
	ResetTasks()
	resource.ResetRepository()
	t.Cleanup(func() {
		resource.SetPlanDraftRecorder(nil)
		plan.SetRecording(false)
		plan.ResetRecord()
	})

	dir := t.TempDir()
	Task("leaf_fail", "leaf whose record fails", func() {
		File(filepath.Join(dir, "out"), options.WithSource(filepath.Join(dir, "does-not-exist.src")))
	})
	Aggregate("innerB", "inner aggregate", "^leaf_fail$")
	Aggregate("outerA", "outer aggregate", "^innerB$")

	_, err := RecordPlan("body_err_chain", testutil.PrivateTempDir(t), "outerA")
	if err == nil {
		t.Fatal("expected the record to fail on the nested aggregate failure")
	}
	if !strings.Contains(err.Error(), "outerA") || !strings.Contains(err.Error(), "innerB") {
		t.Errorf("error should carry the aggregate chain outerA -> innerB, got: %v", err)
	}
	if !strings.Contains(err.Error(), "does-not-exist") {
		t.Errorf("the real cause must remain in the error: %v", err)
	}
}

// TestBodyErrNotLeakedToNextSession pins the per-session reset of
// recordingBodyErr: a failed aggregate record must not poison a subsequent
// healthy Run in the same process.
func TestBodyErrNotLeakedToNextSession(t *testing.T) {
	ResetTasks()
	resource.ResetRepository()
	t.Cleanup(func() {
		resource.SetPlanDraftRecorder(nil)
		plan.SetRecording(false)
		plan.ResetRecord()
	})

	Aggregate("bad_agg", "matches nothing", "^nonexistent$")
	Task("healthy", "healthy task", func() {})

	if _, err := RecordPlan("first", testutil.PrivateTempDir(t), "bad_agg"); err == nil {
		t.Fatal("expected the first record to fail")
	}
	if _, err := RecordPlan("healthy_session", testutil.PrivateTempDir(t), "healthy"); err != nil {
		t.Fatalf("a later healthy session must not inherit the stashed body error: %v", err)
	}
}
