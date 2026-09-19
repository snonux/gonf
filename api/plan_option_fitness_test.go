package api

import (
	"os"
	"os/user"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"syscall"
	"testing"

	opt "github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
	"github.com/snonux/gonf/resource/cmd"
	"github.com/snonux/gonf/resource/cron"
	"github.com/snonux/gonf/resource/dir"
	"github.com/snonux/gonf/resource/file"
	"github.com/snonux/gonf/resource/link"
	"github.com/snonux/gonf/resource/pkg"
	svc "github.com/snonux/gonf/resource/service"
	"github.com/snonux/gonf/resource/systemd"
	"github.com/snonux/gonf/resource/systemdtimer"
	"github.com/snonux/gonf/resource/timer"
)

// This file is the field/option-level counterpart to TestPlanKindFitness
// (api/plan_fitness_test.go), which only proves every plan.Kind round-trips
// through draftToOp — it says nothing about whether a given OPTION on a
// resource actually reaches the wire and back. That gap is exactly how the
// m5 (IsLatest silently dropped by plan recording) and g5 (File
// WithSource(*.tmpl) never rendered via the plan path) bugs shipped: the
// option existed, the kind's fitness fixture existed, but nothing checked
// that a plan.Apply round trip with that option produced the same effect as
// calling the resource directly with it.
//
// Each test below builds a resource TWICE with the identical option set:
// once via the resource's own Ensure (direct, no plan involved) and once by
// recording a Task that uses the same option through the api DSL, encoding
// and decoding the plan, and running plan.Apply — then asserts the two runs
// produced the identical observable effect (the same backend commands, or
// the same filesystem end-state). A future option that is added to a
// resource's PlanDraft/Op fields but forgotten in draftToOp, a Handler's
// ToOp, or the apply-side handler will make its case here diverge instead
// of silently passing.
//
// Coverage is grouped ("kitchen sink" cases bundling several orthogonal
// options, plus dedicated cases for branchy behavior like Absent) rather
// than one case per single option, to keep this file's size manageable
// while still exercising every option each covered kind supports. Extend
// the relevant Test* function when a resource gains a new option.

// recordApplyOption records taskName's body as a one-task plan, encodes and
// decodes it (so the comparison exercises the real wire codec, not just
// draftToOp's in-memory Op), and applies it into planDir. It resets the
// task/resource registries first so option-fitness cases never leak state
// into each other.
func recordApplyOption(t *testing.T, taskName string, build func()) {
	t.Helper()
	ResetTasks()
	resource.ResetRepository()
	t.Cleanup(func() {
		resource.SetPlanDraftRecorder(nil)
		plan.SetRecording(false)
		plan.ResetRecord()
	})

	Task(taskName, "option fitness", build)

	planDir := t.TempDir()
	ops, err := RecordPlan(taskName, planDir, taskName)
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

func argsContainOpt(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// package (resource/pkg): migrated to a plan.Handler (j5). Options: IsAbsent,
// IsLatest — IsLatest is the exact field m5 fixed.
// ---------------------------------------------------------------------------

func TestPlanOptionFitness_Package(t *testing.T) {
	t.Cleanup(pkg.ResetDetectPackageManagerForTest)
	t.Cleanup(pkg.ResetRunCmdForTest)
	pkg.SetDetectPackageManagerForTest(func() (string, error) { return "dnf", nil })

	fakeDNF := func(calls *[][]string) func(name string, args ...string) (string, string, int, error) {
		return func(name string, args ...string) (string, string, int, error) {
			switch name {
			case "rpm":
				// Always report installed: a plain "package" op is then a
				// no-op, but Latest/Absent must still act.
				return "demo-pkg-1.0-1\n", "", 0, nil
			case "dnf":
				*calls = append(*calls, append([]string(nil), args...))
				return "", "", 0, nil
			default:
				return "", "unexpected " + name, 1, nil
			}
		}
	}

	cases := []struct {
		name string
		opts []opt.PackageOption
	}{
		{"Latest", []opt.PackageOption{opt.IsLatest}},
		{"Absent", []opt.PackageOption{opt.IsAbsent}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var directCalls [][]string
			pkg.SetRunCmdForTest(fakeDNF(&directCalls))
			if err := pkg.Ensure("demo-pkg", c.opts...); err != nil {
				t.Fatalf("direct Ensure: %v", err)
			}

			var planCalls [][]string
			pkg.SetRunCmdForTest(fakeDNF(&planCalls))
			recordApplyOption(t, "pkg_opt_"+c.name, func() {
				Package("demo-pkg", c.opts...)
			})

			if !reflect.DeepEqual(directCalls, planCalls) {
				t.Fatalf("%s: plan round-trip diverged from direct Ensure\n direct dnf calls: %v\n plan   dnf calls: %v",
					c.name, directCalls, planCalls)
			}
			if len(planCalls) == 0 {
				t.Fatalf("%s: expected at least one dnf call", c.name)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// cron (resource/cron): migrated to a plan.Handler (j5). Options:
// WithCronUser, WithCommand, WithMinute/Hour/Monthday/Month/Weekday,
// WithCronEnv, IsAbsent.
// ---------------------------------------------------------------------------

func fakeCrontab(tab *string) (
	func(name string, args ...string) (string, string, int, error),
	func(stdin string, name string, args ...string) (string, string, int, error),
) {
	read := func(name string, args ...string) (string, string, int, error) {
		if name != "crontab" {
			return "", "unexpected bin " + name, 1, nil
		}
		if *tab == "" {
			return "", "no crontab for root", 1, nil
		}
		return *tab, "", 0, nil
	}
	write := func(stdin string, name string, args ...string) (string, string, int, error) {
		if name != "crontab" {
			return "", "unexpected bin " + name, 1, nil
		}
		*tab = stdin
		return "", "", 0, nil
	}
	return read, write
}

func TestPlanOptionFitness_Cron(t *testing.T) {
	t.Cleanup(cron.ResetRunnersForTest)

	cases := []struct {
		name string
		opts []opt.CronOption
	}{
		{"KitchenSink", []opt.CronOption{
			opt.WithCronUser("root"),
			opt.WithCommand("/usr/bin/backup"),
			opt.WithMinute("15"),
			opt.WithHour("2"),
			opt.WithMonthday("1"),
			opt.WithMonth("*"),
			opt.WithWeekday("*"),
			opt.WithCronEnv("FOO=1"),
			opt.WithCronEnv("BAR=2"),
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var directTab string
			read, write := fakeCrontab(&directTab)
			cron.SetRunnersForTest(read, write)
			if err := cron.Ensure("optfit", c.opts...); err != nil {
				t.Fatalf("direct Ensure: %v", err)
			}

			var planTab string
			read2, write2 := fakeCrontab(&planTab)
			cron.SetRunnersForTest(read2, write2)
			recordApplyOption(t, "cron_opt_"+c.name, func() {
				Cron("optfit", c.opts...)
			})

			if directTab != planTab {
				t.Fatalf("%s: plan round-trip crontab diverged from direct Ensure\n direct: %q\n plan:   %q", c.name, directTab, planTab)
			}
			if !strings.Contains(planTab, "FOO=1") || !strings.Contains(planTab, "15 2 1 * *") {
				t.Fatalf("%s: crontab missing expected content: %q", c.name, planTab)
			}
		})
	}

	t.Run("Absent", func(t *testing.T) {
		seed := "# BEGIN GONF Cron[optfit]\n7 3 * * * /bin/true\n# END GONF Cron[optfit]\n"

		directTab := seed
		read, write := fakeCrontab(&directTab)
		cron.SetRunnersForTest(read, write)
		if err := cron.Ensure("optfit", opt.IsAbsent); err != nil {
			t.Fatalf("direct Ensure: %v", err)
		}

		planTab := seed
		read2, write2 := fakeCrontab(&planTab)
		cron.SetRunnersForTest(read2, write2)
		recordApplyOption(t, "cron_opt_Absent", func() {
			Cron("optfit", opt.IsAbsent)
		})

		if directTab != planTab {
			t.Fatalf("Absent: plan round-trip crontab diverged from direct Ensure\n direct: %q\n plan:   %q", directTab, planTab)
		}
		if strings.Contains(planTab, "BEGIN GONF Cron[optfit]") {
			t.Fatalf("Absent: job block should have been removed, got %q", planTab)
		}
	})
}

// ---------------------------------------------------------------------------
// service (resource/service): migrated to a plan.Handler (j5). Options:
// WithRestart, WithReload, WithUser, IsAbsent.
// ---------------------------------------------------------------------------

func fakeSystemctl(calls *[][]string, active, enabled bool) func(name string, args ...string) (string, string, int, error) {
	return func(name string, args ...string) (string, string, int, error) {
		if name != "systemctl" {
			return "", "unexpected bin " + name, 1, nil
		}
		switch {
		case argsContainOpt(args, "is-active"):
			if active {
				return "", "", 0, nil
			}
			return "", "", 3, nil
		case argsContainOpt(args, "is-enabled"):
			if enabled {
				return "", "", 0, nil
			}
			return "", "", 1, nil
		}
		*calls = append(*calls, append([]string(nil), args...))
		return "", "", 0, nil
	}
}

func TestPlanOptionFitness_Service(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("systemd service backend is Linux-specific")
	}
	t.Cleanup(svc.ResetRunCmdForTest)

	cases := []struct {
		name            string
		opts            []opt.ServiceOption
		active, enabled bool
	}{
		{"RestartAndUser", []opt.ServiceOption{opt.WithRestart, opt.WithUser}, true, true},
		{"Reload", []opt.ServiceOption{opt.WithReload}, true, true},
		{"EnableAndStart", nil, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var directCalls [][]string
			systemd.SetRunCmdForTest(fakeSystemctl(&directCalls, c.active, c.enabled))
			if err := svc.Ensure("optfitsvc", c.opts...); err != nil {
				t.Fatalf("direct Ensure: %v", err)
			}

			var planCalls [][]string
			systemd.SetRunCmdForTest(fakeSystemctl(&planCalls, c.active, c.enabled))
			recordApplyOption(t, "service_opt_"+c.name, func() {
				Service("optfitsvc", c.opts...)
			})

			if !reflect.DeepEqual(directCalls, planCalls) {
				t.Fatalf("%s: plan round-trip diverged from direct Ensure\n direct: %v\n plan:   %v", c.name, directCalls, planCalls)
			}
		})
	}

	t.Run("Absent", func(t *testing.T) {
		var directCalls [][]string
		systemd.SetRunCmdForTest(fakeSystemctl(&directCalls, true, true))
		if err := svc.Ensure("optfitsvc", opt.IsAbsent); err != nil {
			t.Fatalf("direct Ensure: %v", err)
		}

		var planCalls [][]string
		systemd.SetRunCmdForTest(fakeSystemctl(&planCalls, true, true))
		recordApplyOption(t, "service_opt_Absent", func() {
			Service("optfitsvc", opt.IsAbsent)
		})

		if !reflect.DeepEqual(directCalls, planCalls) {
			t.Fatalf("Absent: plan round-trip diverged from direct Ensure\n direct: %v\n plan:   %v", directCalls, planCalls)
		}
		if !argsContainOpt(flatten(planCalls), "stop") || !argsContainOpt(flatten(planCalls), "disable") {
			t.Fatalf("Absent: expected stop and disable, got %v", planCalls)
		}
	})

	t.Run("OnChangeRestart", func(t *testing.T) {
		var planCalls [][]string
		systemd.SetRunCmdForTest(fakeSystemctl(&planCalls, true, true))
		path := filepath.Join(t.TempDir(), "service.conf")
		recordApplyOption(t, "service_opt_on_change", func() {
			conf := File(path, opt.WithContent("managed\n"))
			Service("optfitsvc", opt.WithRestart, opt.OnChange(conf))
		})
		if !argsContainOpt(flatten(planCalls), "restart") {
			t.Fatalf("OnChange service plan apply did not restart after its managed file changed: %v", planCalls)
		}
	})
}

func flatten(calls [][]string) []string {
	var out []string
	for _, c := range calls {
		out = append(out, c...)
	}
	return out
}

// ---------------------------------------------------------------------------
// timer (resource/timer): NOT migrated to a Handler yet (still on the
// legacy switch path). Options: WithUser, WithRestart, WithEnableOnly,
// IsAbsent.
// ---------------------------------------------------------------------------

func TestPlanOptionFitness_Timer(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("systemd timer backend is Linux-specific")
	}
	t.Cleanup(systemd.ResetRunCmdForTest)

	cases := []struct {
		name            string
		opts            []opt.TimerOption
		active, enabled bool
	}{
		{"RestartAndUser", []opt.TimerOption{opt.WithRestart, opt.WithUser}, true, true},
		{"EnableOnly", []opt.TimerOption{opt.WithEnableOnly}, false, false},
		{"EnableAndStart", nil, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var directCalls [][]string
			systemd.SetRunCmdForTest(fakeSystemctl(&directCalls, c.active, c.enabled))
			if err := timer.Ensure("optfit.timer", c.opts...); err != nil {
				t.Fatalf("direct Ensure: %v", err)
			}

			var planCalls [][]string
			systemd.SetRunCmdForTest(fakeSystemctl(&planCalls, c.active, c.enabled))
			recordApplyOption(t, "timer_opt_"+c.name, func() {
				Timer("optfit.timer", c.opts...)
			})

			if !reflect.DeepEqual(directCalls, planCalls) {
				t.Fatalf("%s: plan round-trip diverged from direct Ensure\n direct: %v\n plan:   %v", c.name, directCalls, planCalls)
			}
		})
	}

	t.Run("Absent", func(t *testing.T) {
		var directCalls [][]string
		systemd.SetRunCmdForTest(fakeSystemctl(&directCalls, true, true))
		if err := timer.Ensure("optfit.timer", opt.IsAbsent); err != nil {
			t.Fatalf("direct Ensure: %v", err)
		}

		var planCalls [][]string
		systemd.SetRunCmdForTest(fakeSystemctl(&planCalls, true, true))
		recordApplyOption(t, "timer_opt_Absent", func() {
			Timer("optfit.timer", opt.IsAbsent)
		})

		if !reflect.DeepEqual(directCalls, planCalls) {
			t.Fatalf("Absent: plan round-trip diverged from direct Ensure\n direct: %v\n plan:   %v", directCalls, planCalls)
		}
	})

	t.Run("OnChangeRestart", func(t *testing.T) {
		var planCalls [][]string
		systemd.SetRunCmdForTest(fakeSystemctl(&planCalls, true, true))
		path := filepath.Join(t.TempDir(), "timer.conf")
		recordApplyOption(t, "timer_opt_on_change", func() {
			conf := File(path, opt.WithContent("managed\n"))
			Timer("optfit.timer", opt.WithRestart, opt.OnChange(conf))
		})
		if !argsContainOpt(flatten(planCalls), "restart") {
			t.Fatalf("OnChange timer plan apply did not restart after its managed file changed: %v", planCalls)
		}
	})
}

// ---------------------------------------------------------------------------
// daemon_reload (resource/systemd): NOT migrated. Options: WithUser,
// IfChanged, WithWatch.
// ---------------------------------------------------------------------------

func TestPlanOptionFitness_DaemonReload(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("systemd daemon-reload is Linux-specific")
	}
	t.Cleanup(systemd.ResetRunCmdForTest)

	fake := func(calls *[][]string) func(name string, args ...string) (string, string, int, error) {
		return func(name string, args ...string) (string, string, int, error) {
			if name != "systemctl" {
				return "", "unexpected bin " + name, 1, nil
			}
			*calls = append(*calls, append([]string(nil), args...))
			return "", "", 0, nil
		}
	}

	var directCalls [][]string
	systemd.SetRunCmdForTest(fake(&directCalls))
	resource.ResetReport()
	if err := systemd.Ensure(opt.WithUser); err != nil {
		t.Fatalf("direct Ensure: %v", err)
	}

	var planCalls [][]string
	systemd.SetRunCmdForTest(fake(&planCalls))
	recordApplyOption(t, "daemon_reload_opt", func() {
		DaemonReload(opt.WithUser)
	})

	if !reflect.DeepEqual(directCalls, planCalls) {
		t.Fatalf("plan round-trip diverged from direct Ensure\n direct: %v\n plan:   %v", directCalls, planCalls)
	}
	if len(planCalls) != 1 || !argsContainOpt(planCalls[0], "--user") {
		t.Fatalf("expected a single --user daemon-reload call, got %v", planCalls)
	}

	t.Run("OnChangeAndLegacyIfChangedWatch", func(t *testing.T) {
		var calls [][]string
		systemd.SetRunCmdForTest(fake(&calls))
		path := filepath.Join(t.TempDir(), "unit.service")
		recordApplyOption(t, "daemon_reload_opt_on_change", func() {
			unit := File(path, opt.WithContent("[Unit]\n"))
			DaemonReload(opt.IfChanged, opt.WithWatch(unit.ID()), opt.OnChange(unit))
		})
		if len(calls) != 1 || !argsContainOpt(calls[0], "daemon-reload") {
			t.Fatalf("OnChange plus legacy IfChanged/WithWatch did not reload after its managed file changed: %v", calls)
		}
	})
}

// ---------------------------------------------------------------------------
// systemd_timer (resource/systemdtimer): NOT migrated. Options: WithCommand,
// WithOnCalendar, WithOnBootSec, WithPersistent, WithDescription,
// WithServiceDescription, WithAfter, WithWants, WithUser, WithRestart,
// WithEnableOnly, IsAbsent. All cases use WithUser so unit files land under
// $HOME/.config/systemd/user instead of the real /etc/systemd/system.
// ---------------------------------------------------------------------------

func TestPlanOptionFitness_SystemdTimer(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("systemd timer backend is Linux-specific")
	}
	t.Cleanup(systemd.ResetRunCmdForTest)

	opts := []opt.SystemdTimerOption{
		opt.WithCommand("/usr/bin/backup"),
		opt.WithOnCalendar("*-*-* *:05:00"),
		opt.WithOnBootSec("10min"),
		opt.WithPersistent,
		opt.WithDescription("fit timer"),
		opt.WithServiceDescription("fit oneshot"),
		opt.WithAfter("network-online.target"),
		opt.WithWants("network-online.target"),
		opt.WithUser,
		opt.WithRestart,
	}

	// systemctl is-active/is-enabled report "not yet installed" so both
	// runs take the enable+start path; every systemctl mutation call is
	// captured for comparison.
	fakeCtl := func(calls *[][]string) func(name string, args ...string) (string, string, int, error) {
		return func(name string, args ...string) (string, string, int, error) {
			if name != "systemctl" {
				return "", "unexpected bin " + name, 1, nil
			}
			switch {
			case argsContainOpt(args, "is-active"), argsContainOpt(args, "is-enabled"):
				return "", "", 1, nil
			}
			*calls = append(*calls, append([]string(nil), args...))
			return "", "", 0, nil
		}
	}

	homeDirect := t.TempDir()
	homePlan := t.TempDir()

	t.Setenv("HOME", homeDirect)
	var directCalls [][]string
	systemd.SetRunCmdForTest(fakeCtl(&directCalls))
	resource.ResetReport()
	if err := systemdtimer.Ensure("optfit", opts...); err != nil {
		t.Fatalf("direct Ensure: %v", err)
	}

	t.Setenv("HOME", homePlan)
	var planCalls [][]string
	systemd.SetRunCmdForTest(fakeCtl(&planCalls))
	recordApplyOption(t, "systemd_timer_opt_KitchenSink", func() {
		SystemdTimer("optfit", opts...)
	})

	if !reflect.DeepEqual(directCalls, planCalls) {
		t.Fatalf("KitchenSink: systemctl calls diverged\n direct: %v\n plan:   %v", directCalls, planCalls)
	}

	directSvc, err := os.ReadFile(filepath.Join(homeDirect, ".config/systemd/user/optfit.service"))
	if err != nil {
		t.Fatalf("direct .service unit missing: %v", err)
	}
	planSvc, err := os.ReadFile(filepath.Join(homePlan, ".config/systemd/user/optfit.service"))
	if err != nil {
		t.Fatalf("plan .service unit missing: %v", err)
	}
	if string(directSvc) != string(planSvc) {
		t.Fatalf("KitchenSink: .service unit diverged\n direct:\n%s\n plan:\n%s", directSvc, planSvc)
	}

	directTimer, err := os.ReadFile(filepath.Join(homeDirect, ".config/systemd/user/optfit.timer"))
	if err != nil {
		t.Fatalf("direct .timer unit missing: %v", err)
	}
	planTimer, err := os.ReadFile(filepath.Join(homePlan, ".config/systemd/user/optfit.timer"))
	if err != nil {
		t.Fatalf("plan .timer unit missing: %v", err)
	}
	if string(directTimer) != string(planTimer) {
		t.Fatalf("KitchenSink: .timer unit diverged\n direct:\n%s\n plan:\n%s", directTimer, planTimer)
	}
	for _, want := range []string{"OnBootSec=10min", "OnCalendar=*-*-* *:05:00", "Persistent=true"} {
		if !strings.Contains(string(planTimer), want) {
			t.Fatalf("KitchenSink: rendered .timer missing %q:\n%s", want, planTimer)
		}
	}
	for _, want := range []string{"Description=fit oneshot", "Wants=network-online.target", "After=network-online.target", "ExecStart=/usr/bin/backup"} {
		if !strings.Contains(string(planSvc), want) {
			t.Fatalf("KitchenSink: rendered .service missing %q:\n%s", want, planSvc)
		}
	}
}

// ---------------------------------------------------------------------------
// file (resource/file): NOT migrated. Options: WithMode, WithOwner,
// WithContent, WithSource(*.tmpl) (the g5 bug's exact field), WithLine,
// WithoutLine, IsAbsent.
// ---------------------------------------------------------------------------

func currentUserAndGroup(t *testing.T) (*user.User, string) {
	t.Helper()
	curr, err := user.Current()
	if err != nil {
		t.Skipf("cannot resolve current user: %v", err)
	}
	gids, err := curr.GroupIds()
	if err != nil || len(gids) == 0 {
		return curr, ""
	}
	for _, gidStr := range gids {
		if gidStr == curr.Gid {
			continue
		}
		g, err := user.LookupGroupId(gidStr)
		if err == nil {
			return curr, g.Name
		}
	}
	return curr, ""
}

func TestPlanOptionFitness_File(t *testing.T) {
	curr, group := currentUserAndGroup(t)

	t.Run("ModeOwnerContent", func(t *testing.T) {
		homeDirect := t.TempDir()
		homePlan := t.TempDir()
		directPath := filepath.Join(homeDirect, "fit.conf")
		planPath := filepath.Join(homePlan, "fit.conf")

		opts := []opt.FileOption{opt.WithContent("fit content\n"), opt.WithMode(0o640), opt.WithOwner(curr.Username)}
		if group != "" {
			opts = append(opts, opt.WithGroup(group))
		}

		if err := file.Ensure(directPath, opts...); err != nil {
			t.Fatalf("direct Ensure: %v", err)
		}
		recordApplyOption(t, "file_opt_ModeOwnerContent", func() {
			File(planPath, opts...)
		})

		assertSameFile(t, directPath, planPath)
	})

	t.Run("TemplateSource", func(t *testing.T) {
		// Pins the g5 fix at the option level: a WithSource(*.tmpl) file
		// must render identically via plan.Apply as it does directly.
		work := t.TempDir()
		src := filepath.Join(work, "app.conf.tmpl")
		if err := os.WriteFile(src, []byte("value={{.Param}}\n"), 0o640); err != nil {
			t.Fatal(err)
		}
		directDst := filepath.Join(work, "direct.conf")
		planDst := filepath.Join(work, "plan.conf")

		if err := file.Ensure(directDst, opt.WithSource(src)); err != nil {
			t.Fatalf("direct Ensure: %v", err)
		}
		recordApplyOption(t, "file_opt_TemplateSource", func() {
			File(planDst, opt.WithSource(src))
		})

		assertSameFile(t, directDst, planDst)
		directData, _ := os.ReadFile(directDst)
		if strings.Contains(string(directData), "{{.Param}}") {
			t.Fatalf("direct render left raw template text: %q", directData)
		}
	})

	t.Run("LineInFile", func(t *testing.T) {
		homeDirect := t.TempDir()
		homePlan := t.TempDir()
		directPath := filepath.Join(homeDirect, "fit.conf")
		planPath := filepath.Join(homePlan, "fit.conf")
		for _, p := range []string{directPath, planPath} {
			if err := os.WriteFile(p, []byte("keep\nold=1\n"), 0o640); err != nil {
				t.Fatal(err)
			}
		}

		opts := []opt.FileOption{opt.WithoutLine("old=1"), opt.WithLine("new=1")}
		if err := file.Ensure(directPath, opts...); err != nil {
			t.Fatalf("direct Ensure: %v", err)
		}
		recordApplyOption(t, "file_opt_LineInFile", func() {
			File(planPath, opts...)
		})

		assertSameFile(t, directPath, planPath)
	})

	t.Run("Absent", func(t *testing.T) {
		homeDirect := t.TempDir()
		homePlan := t.TempDir()
		directPath := filepath.Join(homeDirect, "fit.conf")
		planPath := filepath.Join(homePlan, "fit.conf")
		for _, p := range []string{directPath, planPath} {
			if err := os.WriteFile(p, []byte("x"), 0o640); err != nil {
				t.Fatal(err)
			}
		}

		if err := file.Ensure(directPath, opt.IsAbsent); err != nil {
			t.Fatalf("direct Ensure: %v", err)
		}
		recordApplyOption(t, "file_opt_Absent", func() {
			File(planPath, opt.IsAbsent)
		})

		if _, err := os.Stat(directPath); !os.IsNotExist(err) {
			t.Fatalf("direct Ensure left file behind: %v", err)
		}
		if _, err := os.Stat(planPath); !os.IsNotExist(err) {
			t.Fatalf("plan.Apply left file behind: %v", err)
		}
	})
}

// ---------------------------------------------------------------------------
// dir (resource/dir): NOT migrated. Options: WithMode, WithOwner,
// WithGroup, WithPrune, WithSource, IsAbsent.
// ---------------------------------------------------------------------------

func TestPlanOptionFitness_Dir(t *testing.T) {
	t.Run("ModePrune", func(t *testing.T) {
		srcDirect, srcPlan := t.TempDir(), t.TempDir()
		for _, src := range []string{srcDirect, srcPlan} {
			if err := os.WriteFile(filepath.Join(src, "keep.conf"), []byte("keep\n"), 0o640); err != nil {
				t.Fatal(err)
			}
		}
		dstDirect := filepath.Join(t.TempDir(), "dst")
		dstPlan := filepath.Join(t.TempDir(), "dst")
		if err := os.MkdirAll(dstDirect, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(dstPlan, 0o750); err != nil {
			t.Fatal(err)
		}
		stale := "stale.conf"
		for _, dst := range []string{dstDirect, dstPlan} {
			if err := os.WriteFile(filepath.Join(dst, stale), []byte("stale\n"), 0o640); err != nil {
				t.Fatal(err)
			}
		}

		if err := dir.Ensure(dstDirect, opt.WithMode(0o750), opt.WithSource(srcDirect), opt.WithPrune); err != nil {
			t.Fatalf("direct Ensure: %v", err)
		}
		recordApplyOption(t, "dir_opt_ModePrune", func() {
			Dir(dstPlan, opt.WithMode(0o750), opt.WithSource(srcPlan), opt.WithPrune)
		})

		assertSameFile(t, filepath.Join(dstDirect, "keep.conf"), filepath.Join(dstPlan, "keep.conf"))
		if _, err := os.Stat(filepath.Join(dstDirect, stale)); !os.IsNotExist(err) {
			t.Fatalf("direct Ensure did not prune %s: %v", stale, err)
		}
		if _, err := os.Stat(filepath.Join(dstPlan, stale)); !os.IsNotExist(err) {
			t.Fatalf("plan.Apply did not prune %s: %v", stale, err)
		}
	})

	t.Run("Absent", func(t *testing.T) {
		dstDirect := filepath.Join(t.TempDir(), "dst")
		dstPlan := filepath.Join(t.TempDir(), "dst")
		if err := os.MkdirAll(dstDirect, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(dstPlan, 0o750); err != nil {
			t.Fatal(err)
		}

		if err := dir.Ensure(dstDirect, opt.IsAbsent); err != nil {
			t.Fatalf("direct Ensure: %v", err)
		}
		recordApplyOption(t, "dir_opt_Absent", func() {
			Dir(dstPlan, opt.IsAbsent)
		})

		if _, err := os.Stat(dstDirect); !os.IsNotExist(err) {
			t.Fatalf("direct Ensure left dir behind: %v", err)
		}
		if _, err := os.Stat(dstPlan); !os.IsNotExist(err) {
			t.Fatalf("plan.Apply left dir behind: %v", err)
		}
	})
}

// ---------------------------------------------------------------------------
// ensure_dir (api.EnsureDir): NOT migrated. Options: WithMode, WithOwner,
// WithGroup.
// ---------------------------------------------------------------------------

func TestPlanOptionFitness_EnsureDir(t *testing.T) {
	dstDirect := filepath.Join(t.TempDir(), "ensured")
	dstPlan := filepath.Join(t.TempDir(), "ensured")

	if err := dir.Ensure(dstDirect, opt.WithMode(0o750)); err != nil {
		t.Fatalf("direct Ensure: %v", err)
	}
	recordApplyOption(t, "ensure_dir_opt", func() {
		EnsureDir(dstPlan, opt.WithMode(0o750))
	})

	directInfo, err := os.Stat(dstDirect)
	if err != nil {
		t.Fatal(err)
	}
	planInfo, err := os.Stat(dstPlan)
	if err != nil {
		t.Fatal(err)
	}
	if directInfo.Mode().Perm() != planInfo.Mode().Perm() {
		t.Fatalf("mode diverged: direct=%v plan=%v", directInfo.Mode(), planInfo.Mode())
	}
}

// ---------------------------------------------------------------------------
// link (resource/link): NOT migrated. Options: WithSymlink, WithHardlink,
// IsAbsent.
// ---------------------------------------------------------------------------

func TestPlanOptionFitness_Link(t *testing.T) {
	t.Run("Symlink", func(t *testing.T) {
		target := filepath.Join(t.TempDir(), "target")
		if err := os.WriteFile(target, []byte("x"), 0o640); err != nil {
			t.Fatal(err)
		}
		directPath := filepath.Join(t.TempDir(), "link")
		planPath := filepath.Join(t.TempDir(), "link")

		if err := link.Ensure(directPath, opt.WithSymlink(target)); err != nil {
			t.Fatalf("direct Ensure: %v", err)
		}
		recordApplyOption(t, "link_opt_Symlink", func() {
			Link(planPath, opt.WithSymlink(target))
		})

		directTarget, err := os.Readlink(directPath)
		if err != nil {
			t.Fatal(err)
		}
		planTarget, err := os.Readlink(planPath)
		if err != nil {
			t.Fatal(err)
		}
		if directTarget != planTarget {
			t.Fatalf("symlink target diverged: direct=%q plan=%q", directTarget, planTarget)
		}
	})

	t.Run("Hardlink", func(t *testing.T) {
		target := filepath.Join(t.TempDir(), "target")
		if err := os.WriteFile(target, []byte("x"), 0o640); err != nil {
			t.Fatal(err)
		}
		directPath := filepath.Join(t.TempDir(), "hlink")
		planPath := filepath.Join(t.TempDir(), "hlink")

		if err := link.Ensure(directPath, opt.WithHardlink(target)); err != nil {
			t.Fatalf("direct Ensure: %v", err)
		}
		recordApplyOption(t, "link_opt_Hardlink", func() {
			Link(planPath, opt.WithHardlink(target))
		})

		assertSameFile(t, directPath, planPath)
	})

	t.Run("Absent", func(t *testing.T) {
		directPath := filepath.Join(t.TempDir(), "link")
		planPath := filepath.Join(t.TempDir(), "link")
		target := filepath.Join(t.TempDir(), "target")
		if err := os.WriteFile(target, []byte("x"), 0o640); err != nil {
			t.Fatal(err)
		}
		for _, p := range []string{directPath, planPath} {
			if err := os.Symlink(target, p); err != nil {
				t.Fatal(err)
			}
		}

		if err := link.Ensure(directPath, opt.IsAbsent); err != nil {
			t.Fatalf("direct Ensure: %v", err)
		}
		recordApplyOption(t, "link_opt_Absent", func() {
			Link(planPath, opt.IsAbsent)
		})

		if _, err := os.Lstat(directPath); !os.IsNotExist(err) {
			t.Fatalf("direct Ensure left link behind: %v", err)
		}
		if _, err := os.Lstat(planPath); !os.IsNotExist(err) {
			t.Fatalf("plan.Apply left link behind: %v", err)
		}
	})
}

// ---------------------------------------------------------------------------
// link_if_exists (api.LinkIfExists): NOT migrated (no dedicated resource
// package — the plan_apply.go applyLinkIfExists reimplements the same
// stat-then-link/absent branch this DSL function takes outside recording).
// ---------------------------------------------------------------------------

func TestPlanOptionFitness_LinkIfExists(t *testing.T) {
	t.Run("TargetExists", func(t *testing.T) {
		target := filepath.Join(t.TempDir(), "target")
		if err := os.WriteFile(target, []byte("x"), 0o640); err != nil {
			t.Fatal(err)
		}
		directPath := filepath.Join(t.TempDir(), "link")
		planPath := filepath.Join(t.TempDir(), "link")

		if err := link.Ensure(directPath, opt.WithSymlink(target)); err != nil {
			t.Fatalf("direct: %v", err)
		}
		recordApplyOption(t, "link_if_exists_opt_Exists", func() {
			LinkIfExists(planPath, target)
		})

		directTarget, err := os.Readlink(directPath)
		if err != nil {
			t.Fatal(err)
		}
		planTarget, err := os.Readlink(planPath)
		if err != nil {
			t.Fatal(err)
		}
		if directTarget != planTarget {
			t.Fatalf("target diverged: direct=%q plan=%q", directTarget, planTarget)
		}
	})

	t.Run("TargetMissing", func(t *testing.T) {
		missingTarget := filepath.Join(t.TempDir(), "does-not-exist")
		directPath := filepath.Join(t.TempDir(), "link")
		planPath := filepath.Join(t.TempDir(), "link")

		if err := link.Ensure(directPath, opt.IsAbsent); err != nil {
			t.Fatalf("direct: %v", err)
		}
		recordApplyOption(t, "link_if_exists_opt_Missing", func() {
			LinkIfExists(planPath, missingTarget)
		})

		if _, err := os.Lstat(directPath); !os.IsNotExist(err) {
			t.Fatalf("direct left link behind: %v", err)
		}
		if _, err := os.Lstat(planPath); !os.IsNotExist(err) {
			t.Fatalf("plan.Apply left link behind: %v", err)
		}
	})
}

// ---------------------------------------------------------------------------
// sync_dir (api.SyncDir): NOT migrated. Options: WithFileMode, WithOwner,
// WithGroup, WithPrune, WithSourceBase (implicit via SyncDir's glob dir).
// ---------------------------------------------------------------------------

func TestPlanOptionFitness_SyncDir(t *testing.T) {
	srcDirect, srcPlan := t.TempDir(), t.TempDir()
	for _, src := range []string{srcDirect, srcPlan} {
		if err := os.WriteFile(filepath.Join(src, "app.conf"), []byte("key=value\n"), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	dstDirect := filepath.Join(t.TempDir(), "dst")
	dstPlan := filepath.Join(t.TempDir(), "dst")

	if err := dir.Ensure(dstDirect, opt.WithSource(srcDirect), opt.WithFileMode(0o600), opt.WithPrune); err != nil {
		t.Fatalf("direct: %v", err)
	}
	recordApplyOption(t, "sync_dir_opt", func() {
		SyncDir(dstPlan, filepath.Join(srcPlan, "*.conf"), opt.WithFileMode(0o600), opt.WithPrune)
	})

	assertSameFile(t, filepath.Join(dstDirect, "app.conf"), filepath.Join(dstPlan, "app.conf"))
}

// ---------------------------------------------------------------------------
// command (resource/cmd): NOT migrated. Options: WithName, WithDir,
// WithEnv, Creates, Unless, OnlyIf.
// ---------------------------------------------------------------------------

func TestPlanOptionFitness_Command(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh -c is not portable to windows")
	}

	t.Run("EnvAndDir", func(t *testing.T) {
		dirDirect, dirPlan := t.TempDir(), t.TempDir()
		markerDirect := filepath.Join(dirDirect, "marker")
		markerPlan := filepath.Join(dirPlan, "marker")

		script := []string{"-c", `[ "$FIT_ENV" = "1" ] && touch marker`}
		if err := cmd.Ensure("sh", script,
			opt.WithDir(dirDirect), opt.WithEnv(map[string]string{"FIT_ENV": "1"})); err != nil {
			t.Fatalf("direct: %v", err)
		}
		recordApplyOption(t, "command_opt_EnvAndDir", func() {
			Command("sh", script, opt.WithDir(dirPlan), opt.WithEnv(map[string]string{"FIT_ENV": "1"}))
		})

		if _, err := os.Stat(markerDirect); err != nil {
			t.Fatalf("direct: env var not seen by command: %v", err)
		}
		if _, err := os.Stat(markerPlan); err != nil {
			t.Fatalf("plan: env var not seen by command: %v", err)
		}
	})

	t.Run("CreatesSkips", func(t *testing.T) {
		dirDirect, dirPlan := t.TempDir(), t.TempDir()
		sentinelDirect := filepath.Join(dirDirect, "sentinel")
		sentinelPlan := filepath.Join(dirPlan, "sentinel")
		markerDirect := filepath.Join(dirDirect, "marker")
		markerPlan := filepath.Join(dirPlan, "marker")
		for _, p := range []string{sentinelDirect, sentinelPlan} {
			if err := os.WriteFile(p, []byte("x"), 0o640); err != nil {
				t.Fatal(err)
			}
		}

		if err := cmd.Ensure("touch", []string{markerDirect}, opt.Creates(sentinelDirect)); err != nil {
			t.Fatalf("direct: %v", err)
		}
		recordApplyOption(t, "command_opt_Creates", func() {
			Command("touch", []string{markerPlan}, opt.Creates(sentinelPlan))
		})

		if _, err := os.Stat(markerDirect); !os.IsNotExist(err) {
			t.Fatalf("direct: Creates should have skipped the command, err=%v", err)
		}
		if _, err := os.Stat(markerPlan); !os.IsNotExist(err) {
			t.Fatalf("plan: Creates should have skipped the command, err=%v", err)
		}
	})

	t.Run("UnlessSkipsOnlyIfRuns", func(t *testing.T) {
		dirDirect, dirPlan := t.TempDir(), t.TempDir()
		skippedDirect := filepath.Join(dirDirect, "skipped")
		skippedPlan := filepath.Join(dirPlan, "skipped")
		ranDirect := filepath.Join(dirDirect, "ran")
		ranPlan := filepath.Join(dirPlan, "ran")

		if err := cmd.Ensure("touch", []string{skippedDirect}, opt.Unless("true", nil)); err != nil {
			t.Fatalf("direct unless: %v", err)
		}
		if err := cmd.Ensure("touch", []string{ranDirect}, opt.OnlyIf("true", nil)); err != nil {
			t.Fatalf("direct only_if: %v", err)
		}
		recordApplyOption(t, "command_opt_Guards", func() {
			Command("touch", []string{skippedPlan}, opt.Unless("true", nil))
			Command("touch", []string{ranPlan}, opt.OnlyIf("true", nil))
		})

		if _, err := os.Stat(skippedDirect); !os.IsNotExist(err) {
			t.Fatalf("direct: unless(true) should have skipped touch")
		}
		if _, err := os.Stat(skippedPlan); !os.IsNotExist(err) {
			t.Fatalf("plan: unless(true) should have skipped touch")
		}
		if _, err := os.Stat(ranDirect); err != nil {
			t.Fatalf("direct: only_if(true) should have run touch: %v", err)
		}
		if _, err := os.Stat(ranPlan); err != nil {
			t.Fatalf("plan: only_if(true) should have run touch: %v", err)
		}
	})
}

// assertSameFile compares two regular files' mode bits and content, and (on
// platforms exposing syscall.Stat_t) uid/gid. It fails loudly on any
// mismatch, naming which attribute diverged.
func assertSameFile(t *testing.T, direct, plan string) {
	t.Helper()
	di, err := os.Lstat(direct)
	if err != nil {
		t.Fatalf("direct path missing: %v", err)
	}
	pi, err := os.Lstat(plan)
	if err != nil {
		t.Fatalf("plan path missing: %v", err)
	}
	if di.Mode() != pi.Mode() {
		t.Fatalf("mode diverged: direct=%v plan=%v", di.Mode(), pi.Mode())
	}
	if di.Mode().IsRegular() {
		dd, err := os.ReadFile(direct)
		if err != nil {
			t.Fatal(err)
		}
		pd, err := os.ReadFile(plan)
		if err != nil {
			t.Fatal(err)
		}
		if string(dd) != string(pd) {
			t.Fatalf("content diverged: direct=%q plan=%q", dd, pd)
		}
	}
	dst, dok := di.Sys().(*syscall.Stat_t)
	pst, pok := pi.Sys().(*syscall.Stat_t)
	if dok && pok {
		if dst.Uid != pst.Uid || dst.Gid != pst.Gid {
			t.Fatalf("ownership diverged: direct=%d:%d plan=%d:%d", dst.Uid, dst.Gid, pst.Uid, pst.Gid)
		}
		if dst.Ino == pst.Ino && dst.Dev == pst.Dev {
			// Hardlink case: same inode is the expected match, nothing more to check.
			return
		}
	}
}
