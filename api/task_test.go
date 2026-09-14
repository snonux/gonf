package api

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/snonux/gonf/api/options"
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
	ops, err := RecordPlan("count", t.TempDir(), "all")
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
	ops, err := RecordPlan("count", t.TempDir(), "dia_root")
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

func TestCLIList(t *testing.T) {
	ResetTasks()
	Task("alpha", "first", func() {})
	Task("beta", "second", func() {})

	oldArgs := os.Args
	defer func() { os.Args = oldArgs }()
	os.Args = []string{"gonf", "-list"}

	code := CLI()
	if code != 0 {
		t.Fatalf("CLI exit = %d, want 0", code)
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

func TestCLIRequiresTask(t *testing.T) {
	ResetTasks()
	oldArgs := os.Args
	defer func() { os.Args = oldArgs }()
	os.Args = []string{"gonf"}

	code := CLI()
	if code != 2 {
		t.Fatalf("CLI exit = %d, want 2", code)
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

	_, err := RecordPlan("nested_pack_err", t.TempDir(), "outer_nested")
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
