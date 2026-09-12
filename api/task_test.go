package api

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"github.com/snonux/gonf/api/options"
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
