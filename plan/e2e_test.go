package plan_test

import (
	"context"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/snonux/gonf/api"
	"github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/internal/runners"
	"github.com/snonux/gonf/internal/testseam"
	"github.com/snonux/gonf/internal/testutil"
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

// TestE2EOnChangeRunsOncePerManagedInputChange proves the plan path, not
// merely direct resources: a changed File report reaches a subsequently
// applied Command handler through the recorded IfChanged/Watch fields, and
// a converged second apply holds that command.
func TestE2EOnChangeRunsOncePerManagedInputChange(t *testing.T) {
	api.ResetTasks()
	resource.ResetRepository()
	t.Cleanup(func() {
		resource.SetPlanDraftRecorder(nil)
		plan.SetRecording(false)
		plan.ResetRecord()
	})

	base := testutil.PrivateTempDir(t)
	input := filepath.Join(base, "input.conf")
	output := filepath.Join(base, "reloads")
	api.Task("on_change_e2e", "", func() {
		managed := api.File(input, options.WithContent("managed\n"))
		api.Command("sh", []string{"-c", "printf 'reload\\n' >> " + output}, options.OnChange(managed))
	})

	ops, err := api.RecordPlan("on-change-e2e", base, "on_change_e2e")
	if err != nil {
		t.Fatalf("RecordPlan: %v", err)
	}
	if err := api.ApplyPlan(ops, base); err != nil {
		t.Fatalf("first ApplyPlan: %v", err)
	}
	if err := api.ApplyPlan(ops, base); err != nil {
		t.Fatalf("second ApplyPlan: %v", err)
	}
	got, err := os.ReadFile(output)
	if err != nil {
		t.Fatalf("read gated command output: %v", err)
	}
	if string(got) != "reload\n" {
		t.Fatalf("gated command output = %q, want one run", got)
	}
}

// End-to-end for the cron and service plan kinds: RecordPlan → Encode →
// Decode → plan.Apply, with the crontab and systemctl exec layers faked so no
// real crontab or service manager is touched.
func TestE2ECronAndServicePlanApply(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("service backend fake is systemd-specific")
	}
	current, err := user.Current()
	if err != nil {
		t.Skipf("cannot resolve current user: %v", err)
	}
	api.ResetTasks()
	resource.ResetRepository()
	t.Cleanup(func() {
		resource.SetPlanDraftRecorder(nil)
		plan.SetRecording(false)
		plan.ResetRecord()
	})

	// Fake crontab: starts empty, stores what gonf writes.
	tab := ""
	testseam.FakeCrontab(t, testseam.Crontab{
		Read: func(name string, args ...string) (string, string, int, error) {
			if name != "crontab" {
				return "", "unexpected bin " + name, 1, nil
			}
			if tab == "" {
				return "", "no crontab for root", 1, nil
			}
			return tab, "", 0, nil
		},
		Write: func(stdin string, name string, args ...string) (string, string, int, error) {
			if name != "crontab" {
				return "", "unexpected bin " + name, 1, nil
			}
			tab = stdin
			return "", "", 0, nil
		},
	})

	// Fake systemctl (the systemd backend Service selects on this GOOS==
	// linux-only test, task 4e2's runners.Set injection): service already
	// running + enabled; record mutation calls.
	var ctlCalls [][]string
	sysR := &runners.SystemdRunners{Run: func(name string, args ...string) (string, string, int, error) {
		if name != "systemctl" {
			return "", "unexpected bin " + name, 1, nil
		}
		ctlCalls = append(ctlCalls, append([]string(nil), args...))
		if argsContain(args, "is-active") || argsContain(args, "is-enabled") {
			return "", "", 0, nil
		}
		return "", "", 0, nil
	}}

	api.Task("cron_svc", "cron and service e2e", func() {
		api.Cron("zzjob",
			options.WithCronUser(current.Username),
			options.WithCommand("/bin/true"),
			options.WithMinute("7"),
			options.WithHour("3"),
			options.WithCronEnv("FOO=1"),
		)
		api.Service("zzsvc", options.WithRestart)
	})

	planDir := testutil.PrivateTempDir(t)
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
	ctx := runners.WithSet(context.Background(), &runners.Set{Systemd: sysR})
	if err := plan.ApplyWithContext(ctx, decoded, facts, planDir); err != nil {
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

// TestE2EFileOwnershipPlanApply pins the owner/group wiring end to end:
// RecordPlan → Encode → Decode → plan.Apply must enforce ownership explicitly
// set via WithOwner/WithGroup (owner by name, group by name here, so the
// os/user resolution paths are exercised too) and must carry ownership for
// ensure_dir as well. Chowning to the current user's own uid works
// unprivileged, so the test runs without privileges.
//
// The group is deliberately a SUPPLEMENTARY group of the current user (not
// the primary gid): build()'s apply-side default is user.Current(), so a
// chown-to-primary-gid assertion would be satisfied even if the apply-side
// owner/group wiring were dropped entirely (tautological). A supplementary
// group differs from the default gid, so the assertion only passes when the
// recorded op really reaches the chown call. Skips when the user has no
// supplementary group.
func TestE2EFileOwnershipPlanApply(t *testing.T) {
	curr, err := user.Current()
	if err != nil {
		t.Skipf("cannot resolve current user: %v", err)
	}
	supplementary := supplementaryGroupName(t, curr)
	if supplementary.Name == "" {
		t.Skip("current user has no supplementary group to assert chown wiring with")
	}
	uid, err := strconv.Atoi(curr.Uid)
	if err != nil {
		t.Fatalf("parse current uid %s: %v", curr.Uid, err)
	}
	sgid, err := strconv.Atoi(supplementary.Gid)
	if err != nil {
		t.Fatalf("parse supplementary gid %s: %v", supplementary.Gid, err)
	}

	api.ResetTasks()
	resource.ResetRepository()
	t.Cleanup(func() {
		resource.SetPlanDraftRecorder(nil)
		plan.SetRecording(false)
		plan.ResetRecord()
	})

	home := t.TempDir()
	t.Setenv("HOME", home)
	ownedFile := filepath.Join(home, "owned.conf")
	ownedDir := filepath.Join(home, "owndir")

	api.Task("ownership_e2e", "owner/group e2e", func() {
		api.File(ownedFile,
			options.WithContent("owned"),
			options.WithOwner(curr.Username),
			options.WithGroup(supplementary.Name),
		)
		api.EnsureDir(ownedDir,
			options.WithMode(0o750),
			options.WithOwner(curr.Username),
			options.WithGroup(supplementary.Name),
		)
	})

	planDir := testutil.PrivateTempDir(t)
	ops, err := api.RecordPlan("ownership", planDir, "ownership_e2e")
	if err != nil {
		t.Fatalf("RecordPlan: %v", err)
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

	fileInfo, err := os.Stat(ownedFile)
	if err != nil {
		t.Fatal(err)
	}
	fst, ok := fileInfo.Sys().(*syscall.Stat_t)
	if !ok {
		t.Skipf("no syscall.Stat_t on %s", runtime.GOOS)
	}
	if int(fst.Uid) != uid {
		t.Errorf("owned file uid = %d, want %d (user %s)", fst.Uid, uid, curr.Username)
	}
	if int(fst.Gid) != sgid {
		t.Errorf("owned file gid = %d, want %d (group %s: dropped apply-side owner/group wiring?)", fst.Gid, sgid, supplementary.Name)
	}

	dirInfo, err := os.Stat(ownedDir)
	if err != nil {
		t.Fatal(err)
	}
	dst, ok := dirInfo.Sys().(*syscall.Stat_t)
	if !ok {
		t.Skipf("no syscall.Stat_t on %s", runtime.GOOS)
	}
	if int(dst.Uid) != uid {
		t.Errorf("ensure_dir uid = %d, want %d (user %s)", dst.Uid, uid, curr.Username)
	}
	if int(dst.Gid) != sgid {
		t.Errorf("ensure_dir gid = %d, want %d (group %s)", dst.Gid, sgid, supplementary.Name)
	}
}

// supplementaryGroupName returns a group the user belongs to whose gid
// differs from the primary gid, for chown assertions that cannot be satisfied
// by apply-side defaults. Returns an empty-name group when none exists.
func supplementaryGroupName(t *testing.T, curr *user.User) *user.Group {
	t.Helper()
	gids, err := curr.GroupIds()
	if err != nil {
		return &user.Group{}
	}
	for _, gidStr := range gids {
		if gidStr == curr.Gid {
			continue
		}
		g, err := user.LookupGroupId(gidStr)
		if err != nil {
			continue
		}
		return g
	}
	return &user.Group{}
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
		{Op: plan.KindLink, Path: "${HOME}/.bashrc", Payload: plan.LinkPayload{Symlink: target}},
		{Op: plan.KindWhenBegin, All: []plan.Predicate{{Fact: "goos", Eq: "linux"}}},
		{Op: plan.KindFile, Path: "${HOME}/.taskrc", Mode: "0640", Payload: plan.FilePayload{ContentB64: "Li4u"}},
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
	current, err := user.Current()
	if err != nil {
		t.Skipf("cannot resolve current user: %v", err)
	}
	ops := []plan.Op{
		{Op: plan.KindPlan, Version: plan.CurrentVersion, ID: "cron"},
		{Op: plan.KindCron, Name: "zzjob", Command: "/bin/true"},
	}
	err = plan.Apply(ops, plan.Facts{GOOS: "linux"}, "")
	if err == nil || !strings.Contains(err.Error(), "5 whitespace-separated fields") {
		t.Fatalf("expected missing-schedule error, got %v", err)
	}
	// Absent ops legitimately carry no schedule.
	absent := []plan.Op{
		{Op: plan.KindPlan, Version: plan.CurrentVersion, ID: "cron"},
		{Op: plan.KindCron, Name: "zzjob", Absent: true, Payload: plan.CronPayload{CronUser: current.Username}},
	}
	testseam.FakeCrontab(t, testseam.Crontab{
		Read: func(name string, args ...string) (string, string, int, error) {
			return "", "", 0, nil
		},
		Write: func(stdin string, name string, args ...string) (string, string, int, error) {
			return "", "", 0, nil
		},
	})
	if err := plan.Apply(absent, plan.Facts{GOOS: "linux"}, ""); err != nil {
		t.Fatalf("absent cron without schedule must apply: %v", err)
	}
}

// TestE2ESyncDirOwnershipPlanApply pins the sync_dir ownership wiring end to
// end: the recorded dir owner/group must reach every copied file through
// applySyncDir → dir.Ensure → copySourceFile → file.Ensure. The group is a
// supplementary group of the current user so the assertion cannot be
// satisfied by apply-side defaults (see TestE2EFileOwnershipPlanApply).
func TestE2ESyncDirOwnershipPlanApply(t *testing.T) {
	curr, err := user.Current()
	if err != nil {
		t.Skipf("cannot resolve current user: %v", err)
	}
	supplementary := supplementaryGroupName(t, curr)
	if supplementary.Name == "" {
		t.Skip("current user has no supplementary group to assert chown wiring with")
	}
	uid, err := strconv.Atoi(curr.Uid)
	if err != nil {
		t.Fatalf("parse current uid %s: %v", curr.Uid, err)
	}
	sgid, err := strconv.Atoi(supplementary.Gid)
	if err != nil {
		t.Fatalf("parse supplementary gid %s: %v", supplementary.Gid, err)
	}

	api.ResetTasks()
	resource.ResetRepository()
	t.Cleanup(func() {
		resource.SetPlanDraftRecorder(nil)
		plan.SetRecording(false)
		plan.ResetRecord()
	})

	base := t.TempDir()
	t.Setenv("HOME", base)
	if err := os.Mkdir(filepath.Join(base, "src"), 0o750); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(base, "src", "app.conf")
	if err := os.WriteFile(src, []byte("key=value"), 0o640); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(base, "dst")

	api.Task("syncdir_ownership_e2e", "sync_dir owner/group e2e", func() {
		api.SyncDir(dst, filepath.Join(base, "src", "*.conf"),
			options.WithOwner(curr.Username),
			options.WithGroup(supplementary.Name),
		)
	})

	planDir := testutil.PrivateTempDir(t)
	ops, err := api.RecordPlan("syncdir_ownership", planDir, "syncdir_ownership_e2e")
	if err != nil {
		t.Fatalf("RecordPlan: %v", err)
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

	copied := filepath.Join(dst, "app.conf")
	info, err := os.Stat(copied)
	if err != nil {
		t.Fatalf("sync_dir did not copy %s: %v", copied, err)
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Skipf("no syscall.Stat_t on %s", runtime.GOOS)
	}
	if int(st.Uid) != uid {
		t.Errorf("synced file uid = %d, want %d (user %s)", st.Uid, uid, curr.Username)
	}
	if int(st.Gid) != sgid {
		t.Errorf("synced file gid = %d, want %d (group %s: dropped apply-side owner/group wiring?)", st.Gid, sgid, supplementary.Name)
	}
}

// TestE2ESyncDirTemplateParamStableAcrossPlanDirs pins the sync_dir template
// flap fix (schema v6): a .tmpl entry inside a synced tree must render
// {{.Param}} from the recipe's DECLARED source identity (source_dir +
// relative entry path), never from the ephemeral blob-extraction dir of the
// run. The same task is recorded and applied twice with DIFFERENT plan dirs;
// a blob-path Param would change the rendered content every run (the
// demo_files re-run flap), so the second run must converge: no changed note
// for the rendered files, and the declared param embedded in the content.
// Both sync_dir flavors are covered: the WithSource tree and the
// WithSourceGlob flat install.
func TestE2ESyncDirTemplateParamStableAcrossPlanDirs(t *testing.T) {
	api.ResetTasks()
	resource.ResetRepository()
	t.Cleanup(func() {
		resource.SetPlanDraftRecorder(nil)
		plan.SetRecording(false)
		plan.ResetRecord()
	})

	work := t.TempDir()
	t.Chdir(work)

	if err := os.Mkdir(filepath.Join(work, "src"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "src", "app.conf.tmpl"),
		[]byte("param is {{.Param}}\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "src", "plain.conf"),
		[]byte("plain\n"), 0o640); err != nil {
		t.Fatal(err)
	}

	api.Task("tmpl_sync_e2e", "sync_dir template param e2e", func() {
		api.Dir("tree-dst", options.WithSource("src"))
		api.SyncDir("glob-dst", "src/*.conf.tmpl")
	})

	apply := func(planDir string) {
		t.Helper()
		ops, err := api.RecordPlan("tmpl-sync", planDir, "tmpl_sync_e2e")
		if err != nil {
			t.Fatalf("RecordPlan: %v", err)
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
	}

	// Record and apply with DIFFERENT plan dirs: the blob-extraction root
	// (planDir/blobs/…) differs per run, so the pre-fix Param — derived from
	// the mechanical source path — was a per-run random path.
	planDir1 := filepath.Join(t.TempDir(), "one")
	planDir2 := filepath.Join(t.TempDir(), "two")
	apply(planDir1)
	apply(planDir2)

	want := "param is src/app.conf.tmpl\n"
	rendered := []string{"tree-dst/app.conf", "glob-dst/app.conf"}
	for _, name := range rendered {
		data, err := os.ReadFile(filepath.Join(work, filepath.FromSlash(name)))
		if err != nil {
			t.Fatalf("synced %s: %v", name, err)
		}
		if string(data) != want {
			t.Fatalf("%s rendered content = %q, want %q", name, data, want)
		}
	}
	// The rendered files must NOT have been rewritten by the second apply:
	// their content no longer depends on the per-run blob path. (The first
	// apply created them, so a missing-note outcome would only mask a
	// regression that skips the copy entirely.)
	for _, name := range rendered {
		if resource.AnyChanged("File[" + name + "]") {
			t.Fatalf("%s was rewritten by the second apply (blob-path Param flap)", name)
		}
	}
}

// TestE2EFileTemplateSourceRendersThroughPlan pins the g5 fix at the
// plan-apply level (schema v9's template/template_param fields): a single
// File resource (not a SyncDir tree — see TestE2ESyncDirTemplateParamStableAcrossPlanDirs
// for that already-working path) whose WithSource ends in ".tmpl" must
// render {{.Param}} on the destination during plan.Apply, exactly like a
// direct (non-plan) file.Ensure call would. Before the fix, RecordPlan's
// packageDraft read the raw template bytes into content_b64 with no
// template signal on the op, so plan.Apply's applyFile wrote the literal
// "{{.Param}}" text to the destination instead of rendering it.
//
// {{.Param}} alone is not enough to distinguish "rendered at record time"
// from "rendered at apply time on the destination": it is bound to the
// source path, a value that is identical during RecordPlan and plan.Apply
// in this test (same process). So this test also templates
// {{.GONF_G5_TOKEN}}, an arbitrary env var (applyTemplateToContent seeds
// the template data map from os.Environ(), see resource/file/file.go), and
// sets it to a DIFFERENT value between RecordPlan and plan.Apply — mirroring
// the t.Setenv("HOME", home) / t.Setenv("HOME", home2) pattern used above in
// TestE2EFactWhenBothBranches. The rendered destination must reflect the
// apply-time value, proving rendering genuinely happens on plan.Apply and
// not inside RecordPlan/packageDraft. A regression that pre-rendered the
// ".tmpl" source during packageDraft would bake in the record-time value
// and this assertion would catch it (TestE2EFileTemplateSourceRendersThroughPlan's
// {{.Param}} assertion alone would not, since .Param never changes here).
func TestE2EFileTemplateSourceRendersThroughPlan(t *testing.T) {
	api.ResetTasks()
	resource.ResetRepository()
	t.Cleanup(func() {
		resource.SetPlanDraftRecorder(nil)
		plan.SetRecording(false)
		plan.ResetRecord()
	})

	work := t.TempDir()
	src := filepath.Join(work, "app.conf.tmpl")
	if err := os.WriteFile(src, []byte("value={{.Param}}\ntoken={{.GONF_G5_TOKEN}}\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(work, "app.conf")

	t.Setenv("GONF_G5_TOKEN", "record-time-value")

	api.Task("tmpl_file_e2e", "file template e2e", func() {
		api.File(dst, options.WithSource(src))
	})

	planDir := filepath.Join(work, "plan-out")
	ops, err := api.RecordPlan("tmpl-file", planDir, "tmpl_file_e2e")
	if err != nil {
		t.Fatalf("RecordPlan: %v", err)
	}

	raw, err := plan.EncodePlan(ops)
	if err != nil {
		t.Fatalf("EncodePlan: %v", err)
	}
	decoded, err := plan.DecodePlanBytes(raw)
	if err != nil {
		t.Fatalf("DecodePlanBytes: %v", err)
	}

	// Change the env var AFTER recording but BEFORE applying: only
	// destination-time rendering can pick this up.
	t.Setenv("GONF_G5_TOKEN", "apply-time-value")

	facts := plan.Facts{GOOS: runtime.GOOS, Profile: "test", Hostname: "localhost"}
	if err := plan.Apply(decoded, facts, planDir); err != nil {
		t.Fatalf("plan.Apply: %v", err)
	}

	data, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read rendered file: %v", err)
	}
	want := "value=" + src + "\ntoken=apply-time-value\n"
	if string(data) != want {
		t.Fatalf("rendered content = %q, want %q (raw template text, or a record-time token, means plan.Apply did not render on the destination at apply time)", data, want)
	}
}

// TestE2ESpecialBitsModePlanApply pins the setuid/setgid mode wiring end to
// end: a raw 0o4755-style WithMode value and its Go flag-form equivalent
// (0o750|os.ModeSetuid) must lower to four-digit plan wire modes ("04755",
// "04750"), survive Encode → Decode → parseMode, and land as the setuid bit
// on the applied file (setgid for the directory case, the legit special bit
// for dirs). Unprivileged apply-side chown clears the special bits on
// non-directories, so the assertion only passes when the apply-side chmod
// runs after the chown — exactly the order applyAttributesTo must keep.
func TestE2ESpecialBitsModePlanApply(t *testing.T) {
	api.ResetTasks()
	resource.ResetRepository()
	t.Cleanup(func() {
		resource.SetPlanDraftRecorder(nil)
		plan.SetRecording(false)
		plan.ResetRecord()
	})

	home := t.TempDir()
	t.Setenv("HOME", home)
	setuidFile := filepath.Join(home, "helper.sh")
	setuidFlagFile := filepath.Join(home, "flag-helper.sh")
	setgidDir := filepath.Join(home, "groupdir")

	api.Task("special_bits_e2e", "setuid/setgid e2e", func() {
		api.File(setuidFile,
			options.WithContent("#!/bin/sh\n"),
			options.WithMode(0o4755),
		)
		api.File(setuidFlagFile,
			options.WithContent("#!/bin/sh\n"),
			options.WithMode(0o750|os.ModeSetuid),
		)
		api.EnsureDir(setgidDir,
			options.WithMode(0o2755),
		)
	})

	planDir := testutil.PrivateTempDir(t)
	ops, err := api.RecordPlan("specialbits", planDir, "special_bits_e2e")
	if err != nil {
		t.Fatalf("RecordPlan: %v", err)
	}

	// The recorded ops must carry the four-digit wire modes.
	wireModes := map[string]string{
		setuidFile:     "04755",
		setuidFlagFile: "04750",
		setgidDir:      "02755",
	}
	saw := map[string]bool{}
	for _, op := range ops {
		want, ok := wireModes[op.Path]
		if !ok {
			continue
		}
		if op.Mode != want {
			t.Fatalf("%s op for %s mode = %q, want %q (setuid/setgid masked away on the wire?)", op.Op, op.Path, op.Mode, want)
		}
		saw[op.Path] = true
	}
	for path := range wireModes {
		if !saw[path] {
			t.Fatalf("no recorded op with mode for %s", path)
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

	fileInfo, err := os.Stat(setuidFile)
	if err != nil {
		t.Fatalf("applied file missing: %v", err)
	}
	if got := fileInfo.Mode().Perm(); got != 0o755 {
		t.Errorf("applied file perm = %#o, want 0755", got)
	}
	if fileInfo.Mode()&os.ModeSetuid == 0 {
		t.Errorf("applied file mode %v has no setuid bit (dropped by chmod/chown ordering?)", fileInfo.Mode())
	}

	dirInfo, err := os.Stat(setgidDir)
	if err != nil {
		t.Fatalf("applied dir missing: %v", err)
	}
	if got := dirInfo.Mode().Perm(); got != 0o755 {
		t.Errorf("applied dir perm = %#o, want 0755", got)
	}
	if dirInfo.Mode()&os.ModeSetgid == 0 {
		t.Errorf("applied dir mode %v has no setgid bit", dirInfo.Mode())
	}

	flagFileInfo, err := os.Stat(setuidFlagFile)
	if err != nil {
		t.Fatalf("flag-form applied file missing: %v", err)
	}
	if got := flagFileInfo.Mode().Perm(); got != 0o750 {
		t.Errorf("flag-form applied file perm = %#o, want 0750", got)
	}
	if flagFileInfo.Mode()&os.ModeSetuid == 0 {
		t.Errorf("flag-form applied file mode %v has no setuid bit", flagFileInfo.Mode())
	}
}
