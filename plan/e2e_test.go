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
	"github.com/snonux/gonf/resource/cron"
	"github.com/snonux/gonf/resource/service"
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

// End-to-end for the cron and service plan kinds: RecordPlan → Encode →
// Decode → plan.Apply, with the crontab and systemctl exec layers faked so no
// real crontab or service manager is touched.
func TestE2ECronAndServicePlanApply(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("service backend fake is systemd-specific")
	}
	api.ResetTasks()
	resource.ResetRepository()
	cron.ResetRunnersForTest()
	service.ResetRunCmdForTest()
	t.Cleanup(func() {
		cron.ResetRunnersForTest()
		service.ResetRunCmdForTest()
		resource.SetPlanDraftRecorder(nil)
		plan.SetRecording(false)
		plan.ResetRecord()
	})

	// Fake crontab: starts empty, stores what gonf writes.
	tab := ""
	cron.SetRunnersForTest(
		func(name string, args ...string) (string, string, int, error) {
			if name != "crontab" {
				return "", "unexpected bin " + name, 1, nil
			}
			if tab == "" {
				return "", "no crontab for root", 1, nil
			}
			return tab, "", 0, nil
		},
		func(stdin string, name string, args ...string) (string, string, int, error) {
			if name != "crontab" {
				return "", "unexpected bin " + name, 1, nil
			}
			tab = stdin
			return "", "", 0, nil
		},
	)

	// Fake systemctl: service already running + enabled; record mutation calls.
	var ctlCalls [][]string
	service.SetRunCmdForTest(func(name string, args ...string) (string, string, int, error) {
		if name != "systemctl" {
			return "", "unexpected bin " + name, 1, nil
		}
		ctlCalls = append(ctlCalls, append([]string(nil), args...))
		if argsContain(args, "is-active") || argsContain(args, "is-enabled") {
			return "", "", 0, nil
		}
		return "", "", 0, nil
	})

	api.Task("cron_svc", "cron and service e2e", func() {
		api.Cron("zzjob",
			options.WithCommand("/bin/true"),
			options.WithMinute("7"),
			options.WithHour("3"),
			options.WithCronEnv("FOO=1"),
		)
		api.Service("zzsvc", options.WithRestart)
	})

	planDir := t.TempDir()
	ops, err := api.RecordPlan("cronsvc", planDir, "cron_svc")
	if err != nil {
		t.Fatalf("RecordPlan: %v", err)
	}
	wantKinds := []plan.Kind{plan.KindPlan, plan.KindCron, plan.KindService}
	if len(ops) != len(wantKinds) {
		t.Fatalf("ops kinds = %v", ops)
	}
	for i, k := range wantKinds {
		if ops[i].Op != k {
			t.Fatalf("ops[%d] = %s, want %s", i, ops[i].Op, k)
		}
	}

	raw, err := plan.EncodePlan(ops)
	if err != nil {
		t.Fatalf("EncodePlan: %v", err)
	}
	decoded, err := plan.DecodePlanBytes(raw)
	if err != nil {
		t.Fatalf("DecodePlanBytes: %v", err)
	}

	facts := plan.Facts{GOOS: runtime.GOOS, Profile: "test", Hostname: "localhost"}
	if err := plan.Apply(decoded, facts, planDir); err != nil {
		t.Fatalf("plan.Apply: %v", err)
	}

	// The cron op must have installed the job block in the (fake) crontab.
	for _, needle := range []string{
		"# BEGIN GONF Cron[zzjob]",
		"FOO=1",
		"7 3 * * * /bin/true",
		"# END GONF Cron[zzjob]",
	} {
		if !strings.Contains(tab, needle) {
			t.Fatalf("crontab missing %q; got:\n%s", needle, tab)
		}
	}

	// The service op must have gone through Ensure; zzsvc was already active
	// and enabled, so WithRestart must surface as a restart action.
	var sawRestart bool
	for _, args := range ctlCalls {
		if argsContain(args, "restart") && argsContain(args, "zzsvc") {
			sawRestart = true
		}
	}
	if !sawRestart {
		t.Fatalf("expected systemctl restart for zzsvc, calls: %v", ctlCalls)
	}
}

func argsContain(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
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

// TestApplyCronRejectsMissingSchedule pins the corrupt-plan guard: a present
// cron op without a 5-field schedule must fail loudly instead of silently
// falling back to the every-minute default schedule. Nothing reaches the
// crontab backend because the check fires before cron.Ensure.
func TestApplyCronRejectsMissingSchedule(t *testing.T) {
	ops := []plan.Op{
		{Op: plan.KindPlan, Version: plan.CurrentVersion, ID: "cron"},
		{Op: plan.KindCron, Name: "zzjob", Command: "/bin/true"},
	}
	err := plan.Apply(ops, plan.Facts{GOOS: "linux"}, "")
	if err == nil || !strings.Contains(err.Error(), "5 whitespace-separated fields") {
		t.Fatalf("expected missing-schedule error, got %v", err)
	}
	// Absent ops legitimately carry no schedule.
	absent := []plan.Op{
		{Op: plan.KindPlan, Version: plan.CurrentVersion, ID: "cron"},
		{Op: plan.KindCron, Name: "zzjob", Absent: true},
	}
	cron.ResetRunnersForTest()
	service.ResetRunCmdForTest()
	t.Cleanup(func() {
		cron.ResetRunnersForTest()
		service.ResetRunCmdForTest()
	})
	cron.SetRunnersForTest(
		func(name string, args ...string) (string, string, int, error) {
			return "", "", 0, nil
		},
		func(stdin string, name string, args ...string) (string, string, int, error) {
			return "", "", 0, nil
		},
	)
	if err := plan.Apply(absent, plan.Facts{GOOS: "linux"}, ""); err != nil {
		t.Fatalf("absent cron without schedule must apply: %v", err)
	}
}
