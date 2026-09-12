package plan_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/snonux/gonf/api"
	"github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
)

// End-to-end: RecordPlan → Encode/Decode → Apply with a temp HOME, covering
// conditionals, guards, and content from the overall remote-plan design.
func TestE2ERecordApplyConditionalsAndContent(t *testing.T) {
	api.ResetTasks()
	resource.ResetRepository()
	t.Cleanup(func() {
		resource.SetPlanDraftRecorder(nil)
		plan.SetRecording(false)
		plan.ResetRecord()
		resource.SetDryRun(false)
	})

	home := t.TempDir()
	t.Setenv("HOME", home)

	dot := filepath.Join(home, "dotfiles")
	if err := os.MkdirAll(filepath.Join(dot, "bash"), 0o750); err != nil {
		t.Fatal(err)
	}
	bashrc := filepath.Join(dot, "bash", "bashrc")
	if err := os.WriteFile(bashrc, []byte("alias ll=ls\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	notesCmd := filepath.Join(home, "Notes", "prompts", "commands")
	if err := os.MkdirAll(notesCmd, 0o750); err != nil {
		t.Fatal(err)
	}
	syncSrc := filepath.Join(home, "src-units")
	if err := os.MkdirAll(syncSrc, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(syncSrc, "x.service"), []byte("[Unit]\n"), 0o640); err != nil {
		t.Fatal(err)
	}

	api.Task("e2e_home", "e2e bundle", func() {
		api.Link(filepath.Join(home, ".bashrc"), options.WithSymlink(bashrc))
		api.WhenPathExists(notesCmd, func() {
			api.EnsureDir(filepath.Join(home, ".cursor"), options.WithMode(0o750))
			api.Link(filepath.Join(home, ".cursor", "commands"), options.WithSymlink(notesCmd))
		})
		api.LinkIfExists(filepath.Join(home, "QuickEdit", "Notes"), filepath.Join(home, "Notes"))
		api.InstallFile(filepath.Join(home, ".taskrc"), filepath.Join(home, "taskrc.src"))
		api.SyncDir(filepath.Join(home, ".config", "systemd", "user"), filepath.Join(syncSrc, "*"), options.WithPrune)
		api.Command("touch", []string{filepath.Join(home, "ran")},
			options.Unless("false", nil),
		)
		api.Command("touch", []string{filepath.Join(home, "skipped")},
			options.Unless("true", nil),
		)
	})

	if err := os.WriteFile(filepath.Join(home, "taskrc.src"), []byte("verbose=1\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, "QuickEdit"), 0o700); err != nil {
		t.Fatal(err)
	}

	planDir := filepath.Join(home, "plan-out")
	ops, err := api.RecordPlan("e2e", planDir, "e2e_home")
	if err != nil {
		t.Fatalf("RecordPlan: %v", err)
	}
	raw, err := plan.EncodePlan(ops)
	if err != nil {
		t.Fatal(err)
	}
	planPath := filepath.Join(planDir, "plan.jsonl")
	if err := os.WriteFile(planPath, raw, 0o640); err != nil {
		t.Fatal(err)
	}

	decoded, err := plan.DecodePlanBytes(raw)
	if err != nil {
		t.Fatal(err)
	}

	facts := plan.Facts{GOOS: runtime.GOOS, Profile: "fedora", Hostname: "earth"}
	if err := plan.Apply(decoded, facts, planDir); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if got, err := os.Readlink(filepath.Join(home, ".bashrc")); err != nil || got != bashrc {
		t.Fatalf("bashrc link: got %q err %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(home, ".cursor", "commands")); err != nil {
		t.Fatalf("agents path gate should link commands: %v", err)
	}
	if got, err := os.Readlink(filepath.Join(home, "QuickEdit", "Notes")); err != nil || !strings.HasSuffix(got, "Notes") {
		t.Fatalf("link_if_exists Notes: %q %v", got, err)
	}
	data, err := os.ReadFile(filepath.Join(home, ".taskrc"))
	if err != nil || string(data) != "verbose=1\n" {
		t.Fatalf("taskrc: %q %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(home, ".config", "systemd", "user", "x.service")); err != nil {
		t.Fatalf("sync_dir blob restore: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "ran")); err != nil {
		t.Fatalf("unless false should run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "skipped")); !os.IsNotExist(err) {
		t.Fatal("unless true should skip")
	}
}

func TestE2EFactWhenBothBranches(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	linuxOnly := filepath.Join("${HOME}", "linux-only")
	ops := []plan.Op{
		{Op: plan.KindPlan, Version: plan.CurrentVersion, ID: "facts"},
		{Op: plan.KindWhenBegin, All: []plan.Predicate{{Fact: "goos", Eq: "linux"}}},
		{Op: plan.KindEnsureDir, Path: linuxOnly, Mode: "0750"},
		{Op: plan.KindWhenEnd},
	}

	expanded := filepath.Join(home, "linux-only")
	if err := plan.Apply(ops, plan.Facts{GOOS: "linux"}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(expanded); err != nil {
		t.Fatal(err)
	}

	home2 := t.TempDir()
	t.Setenv("HOME", home2)
	if err := plan.Apply(ops, plan.Facts{GOOS: "darwin"}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home2, "linux-only")); !os.IsNotExist(err) {
		t.Fatal("darwin must skip linux-only dir")
	}
}

func TestE2EGoldenMiniApplyWithTempTargets(t *testing.T) {
	// Adapt mini_e2e shape with local targets under temp HOME (goldens point at
	// absolute laptop paths that are not portable for apply).
	home := t.TempDir()
	t.Setenv("HOME", home)
	target := filepath.Join(home, "dot", "bashrc")
	if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("rc\n"), 0o640); err != nil {
		t.Fatal(err)
	}

	ops := []plan.Op{
		{Op: plan.KindPlan, Version: plan.CurrentVersion, ID: "demo"},
		{Op: plan.KindLink, Path: "${HOME}/.bashrc", Symlink: target},
		{Op: plan.KindWhenBegin, All: []plan.Predicate{{Fact: "goos", Eq: "linux"}}},
		{Op: plan.KindFile, Path: "${HOME}/.taskrc", Mode: "0640", ContentB64: "Li4u"},
		{Op: plan.KindWhenEnd},
	}
	if err := plan.Apply(ops, plan.Facts{GOOS: runtime.GOOS}, ""); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "linux" {
		data, err := os.ReadFile(filepath.Join(home, ".taskrc"))
		if err != nil || string(data) != "..." {
			t.Fatalf("taskrc %q %v", data, err)
		}
	}
	if got, err := os.Readlink(filepath.Join(home, ".bashrc")); err != nil || got != target {
		t.Fatalf("link %q %v", got, err)
	}
}
