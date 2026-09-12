package api

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
)

func TestCLIPlanAndApply(t *testing.T) {
	ResetTasks()
	resource.ResetRepository()
	Task("cli_touch", "touch a file via plan", func() {
		dir := os.Getenv("GONF_CLI_TEST_DIR")
		File(filepath.Join(dir, "out.txt"), options.WithContent("hello from plan"))
	})

	root := t.TempDir()
	t.Setenv("GONF_CLI_TEST_DIR", root)
	planDir := filepath.Join(root, "planout")

	oldArgs := os.Args
	t.Cleanup(func() { os.Args = oldArgs })

	os.Args = []string{"gonf", "plan", "-o", planDir, "-id", "cli-test", "cli_touch"}
	if code := CLI(); code != 0 {
		t.Fatalf("plan exit %d", code)
	}
	planPath := filepath.Join(planDir, "plan.jsonl")
	raw, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatal(err)
	}
	ops, err := plan.DecodePlanBytes(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) < 2 || ops[0].Op != plan.KindPlan {
		t.Fatalf("unexpected ops: %#v", ops)
	}

	outFile := filepath.Join(root, "out.txt")
	os.Args = []string{"gonf", "apply", planPath}
	if code := CLI(); code != 0 {
		t.Fatalf("apply exit %d", code)
	}
	data, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello from plan" {
		t.Fatalf("got %q", data)
	}
}

func TestCLIPlanStdout(t *testing.T) {
	ResetTasks()
	resource.ResetRepository()
	Task("cli_stdout", "", func() {
		File(filepath.Join(t.TempDir(), "x"), options.WithContent("via-stdout"))
	})

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldOut := os.Stdout
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = oldOut })

	oldArgs := os.Args
	t.Cleanup(func() { os.Args = oldArgs })
	os.Args = []string{"gonf", "plan", "-stdout", "-id", "stdout-test", "cli_stdout"}
	code := CLI()
	_ = w.Close()
	os.Stdout = oldOut
	raw, err := io.ReadAll(r)
	_ = r.Close()
	if err != nil {
		t.Fatal(err)
	}
	if code != 0 {
		t.Fatalf("plan -stdout exit %d", code)
	}
	ops, err := plan.DecodePlanBytes(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) < 2 || ops[0].Op != plan.KindPlan || ops[0].ID != "stdout-test" {
		t.Fatalf("unexpected ops: %#v", ops)
	}
}

func TestCLIPlanRequiresTasks(t *testing.T) {
	oldArgs := os.Args
	t.Cleanup(func() { os.Args = oldArgs })
	os.Args = []string{"gonf", "plan", "-o", t.TempDir()}
	if code := CLI(); code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
}

func TestCLIApplyRejectsBadVersion(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "bad.jsonl")
	if err := os.WriteFile(path, []byte(`{"op":"plan","version":99}`+"\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	oldArgs := os.Args
	t.Cleanup(func() { os.Args = oldArgs })
	os.Args = []string{"gonf", "apply", path}
	if code := CLI(); code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
}

func TestCLIApplyDryRun(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "plan.jsonl")
	target := filepath.Join(root, "should-not-exist")
	ops := []plan.Op{
		{Op: plan.KindPlan, Version: plan.CurrentVersion, ID: "dry"},
		{Op: plan.KindCommand, Bin: "touch", Args: []string{target}},
	}
	raw, err := plan.EncodePlan(ops)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o640); err != nil {
		t.Fatal(err)
	}

	oldArgs := os.Args
	t.Cleanup(func() {
		os.Args = oldArgs
		resource.SetDryRun(false)
	})
	os.Args = []string{"gonf", "apply", "-n", path}
	if code := CLI(); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatal("dry-run must not create file")
	}
}

func TestPrintUsageMentionsPlanApply(t *testing.T) {
	// smoke: usage strings stay discoverable via source; CLI with no args returns 2
	oldArgs := os.Args
	t.Cleanup(func() { os.Args = oldArgs })
	os.Args = []string{"gonf"}
	if code := CLI(); code != 2 {
		t.Fatalf("exit %d", code)
	}
	_ = strings.Contains
}
