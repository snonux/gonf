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

func TestRecordPlanLowersWhenEnsureDirLinkIfExists(t *testing.T) {
	ResetTasks()
	resource.ResetRepository()
	t.Cleanup(func() {
		resource.SetPlanDraftRecorder(nil)
		plan.SetRecording(false)
		plan.ResetRecord()
	})

	dir := t.TempDir()
	cursor := filepath.Join(dir, ".cursor")
	notes := filepath.Join(dir, "Notes")
	linkPath := filepath.Join(dir, "QuickEdit", "Notes")

	// WhenLinux must emit when_begin even if we force a non-matching Activate filter.
	Activate(Facts{GOOS: "windows", Profile: "unknown"})

	Task("home_gated", "linux gated", func() {
		EnsureDir(cursor, options.WithMode(0o750))
		LinkIfExists(linkPath, notes)
		Package("fish")
	}, WhenLinux(), WhenProfile("fedora"))

	ops, err := RecordPlan("gated", "", "home_gated")
	if err != nil {
		t.Fatalf("RecordPlan: %v", err)
	}

	wantKinds := []plan.Kind{
		plan.KindPlan,
		plan.KindWhenBegin,
		plan.KindEnsureDir,
		plan.KindLinkIfExists,
		plan.KindPackage,
		plan.KindWhenEnd,
	}
	if len(ops) != len(wantKinds) {
		t.Fatalf("ops=%v want %d kinds", ops, len(wantKinds))
	}
	for i, k := range wantKinds {
		if ops[i].Op != k {
			t.Fatalf("ops[%d]=%s want %s (all %#v)", i, ops[i].Op, k, ops)
		}
	}

	begin := ops[1]
	if begin.ID != "when.home_gated" || len(begin.All) != 2 {
		t.Fatalf("when_begin = %#v", begin)
	}
	if !reflect.DeepEqual(begin.All[0], plan.Predicate{Fact: "goos", Eq: "linux"}) {
		t.Fatalf("pred0 = %#v", begin.All[0])
	}
	if !reflect.DeepEqual(begin.All[1], plan.Predicate{Fact: "profile", Eq: "fedora"}) {
		t.Fatalf("pred1 = %#v", begin.All[1])
	}

	ensure := ops[2]
	if ensure.Path != cursor || ensure.Mode != "0750" {
		t.Fatalf("ensure_dir = %#v", ensure)
	}
	if _, err := os.Stat(cursor); !os.IsNotExist(err) {
		t.Fatalf("ensure_dir must not create on controller: %v", err)
	}

	linkOp := ops[3]
	if linkOp.Path != linkPath || linkOp.Target != notes {
		t.Fatalf("link_if_exists = %#v", linkOp)
	}
}

func TestRecordPlanWhenPathExistsHelper(t *testing.T) {
	ResetTasks()
	resource.ResetRepository()
	t.Cleanup(func() {
		resource.SetPlanDraftRecorder(nil)
		plan.SetRecording(false)
		plan.ResetRecord()
	})

	dir := t.TempDir()
	gate := filepath.Join(dir, "commands")
	cursor := filepath.Join(dir, ".cursor")

	Task("home_agents", "", func() {
		WhenPathExists(gate, func() {
			EnsureDir(cursor, options.WithMode(0o750))
			Link(filepath.Join(cursor, "commands"), options.WithSymlink(gate))
		})
	})

	ops, err := RecordPlan("agents", "", "home_agents")
	if err != nil {
		t.Fatalf("RecordPlan: %v", err)
	}

	wantKinds := []plan.Kind{
		plan.KindPlan,
		plan.KindWhenBegin,
		plan.KindEnsureDir,
		plan.KindLink,
		plan.KindWhenEnd,
	}
	if len(ops) != len(wantKinds) {
		t.Fatalf("ops kinds=%v", opsKinds(ops))
	}
	for i, k := range wantKinds {
		if ops[i].Op != k {
			t.Fatalf("ops[%d]=%s want %s", i, ops[i].Op, k)
		}
	}
	if len(ops[1].All) != 1 || ops[1].All[0].PathExists != gate {
		t.Fatalf("path_exists when = %#v", ops[1])
	}
	if _, err := os.Stat(cursor); !os.IsNotExist(err) {
		t.Fatalf("should not create cursor on controller: %v", err)
	}
}

func TestWhenPathExistsLocalRunsOnlyIfPresent(t *testing.T) {
	ResetTasks()
	resource.ResetRepository()
	dir := t.TempDir()
	missing := filepath.Join(dir, "nope")
	present := filepath.Join(dir, "yes")
	if err := os.Mkdir(present, 0o750); err != nil {
		t.Fatal(err)
	}

	var ranMissing, ranPresent bool
	WhenPathExists(missing, func() { ranMissing = true })
	WhenPathExists(present, func() { ranPresent = true })
	if ranMissing {
		t.Fatal("missing path should skip")
	}
	if !ranPresent {
		t.Fatal("present path should run")
	}
}

func TestRecordPlanOpaqueWhenRequiresLocalPass(t *testing.T) {
	ResetTasks()
	Task("custom", "", func() {
		Package("x")
	}, When(func(f Facts) bool { return f.GOOS == "plan9" }))

	_, err := RecordPlan("p", "", "custom")
	if err == nil {
		t.Fatal("expected error for failing opaque When")
	}
}

func TestLinkIfExistsDoesNotProbeDuringRecord(t *testing.T) {
	ResetTasks()
	resource.ResetRepository()
	t.Cleanup(func() {
		resource.SetPlanDraftRecorder(nil)
		plan.SetRecording(false)
		plan.ResetRecord()
	})

	dir := t.TempDir()
	path := filepath.Join(dir, "link")
	target := filepath.Join(dir, "missing-target")

	Task("map", "", func() {
		LinkIfExists(path, target)
	})
	ops, err := RecordPlan("p", "", "map")
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 2 || ops[1].Op != plan.KindLinkIfExists {
		t.Fatalf("ops=%#v", ops)
	}
	if ops[1].Target != target {
		t.Fatalf("target=%q", ops[1].Target)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("must not create NoLink on controller: %v", err)
	}
}

func TestRecordPlanLowersCronAndService(t *testing.T) {
	ResetTasks()
	resource.ResetRepository()
	t.Cleanup(func() {
		resource.SetPlanDraftRecorder(nil)
		plan.SetRecording(false)
		plan.ResetRecord()
	})

	filePath := filepath.Join(t.TempDir(), "marker")

	Task("cron_svc", "cron and service lowering", func() {
		Cron("zzjob",
			options.WithCommand("true"),
			options.WithMinute("7"),
			options.WithHour("3"),
			options.WithCronEnv("FOO=1"),
		)
		Service("zzsvc", options.WithRestart)
		File(filePath, options.WithContent("x"))
	})

	ops, err := RecordPlan("cronsvc", "", "cron_svc")
	if err != nil {
		t.Fatalf("RecordPlan: %v", err)
	}

	wantKinds := []plan.Kind{
		plan.KindPlan,
		plan.KindCron,
		plan.KindService,
		plan.KindFile,
	}
	if !reflect.DeepEqual(opsKinds(ops), wantKinds) {
		t.Fatalf("ops kinds = %v, want %v", opsKinds(ops), wantKinds)
	}

	cronOp := ops[1]
	wantID := "Cron[root/zzjob]"
	if cronOp.ID != wantID {
		t.Fatalf("cron id = %q, want %q (IDs must stay stable for DependsOn)", cronOp.ID, wantID)
	}
	if cronOp.Name != "zzjob" || cronOp.CronUser != "root" || cronOp.Command != "true" {
		t.Fatalf("cron payload = %#v", cronOp)
	}
	if cronOp.Schedule != "7 3 * * *" {
		t.Fatalf("cron schedule = %q, want %q", cronOp.Schedule, "7 3 * * *")
	}
	if !reflect.DeepEqual(cronOp.CronEnv, []string{"FOO=1"}) {
		t.Fatalf("cron env = %#v", cronOp.CronEnv)
	}

	svcOp := ops[2]
	if svcOp.ID != "Service[zzsvc]" || svcOp.Name != "zzsvc" || !svcOp.Restart {
		t.Fatalf("service payload = %#v", svcOp)
	}

	// Wire round-trip must preserve the new kinds and payloads.
	raw, err := plan.EncodePlan(ops)
	if err != nil {
		t.Fatalf("EncodePlan: %v", err)
	}
	decoded, err := plan.DecodePlanBytes(raw)
	if err != nil {
		t.Fatalf("DecodePlanBytes: %v", err)
	}
	if !reflect.DeepEqual(decoded, ops) {
		t.Fatalf("round-trip mismatch\ngot  %#v\nwant %#v", decoded, ops)
	}
}

func TestRecordPlanLowersFileAndDirOwnership(t *testing.T) {
	ResetTasks()
	resource.ResetRepository()
	t.Cleanup(func() {
		resource.SetPlanDraftRecorder(nil)
		plan.SetRecording(false)
		plan.ResetRecord()
	})

	base := t.TempDir()
	ownedFile := filepath.Join(base, "owned.conf")
	unsetFile := filepath.Join(base, "unset.conf")
	absentFile := filepath.Join(base, "absent.conf")
	ownedDir := filepath.Join(base, "owneddir")
	syncDst := filepath.Join(base, "syncdst")
	ensurePath := filepath.Join(base, "ensuredir")

	Task("ownership", "file/dir owner/group lowering", func() {
		File(ownedFile,
			options.WithContent("x"),
			options.WithOwner("daemon"),
			options.WithGroup("1"),
		)
		File(unsetFile, options.WithContent("y"))
		NoFile(absentFile)
		Dir(ownedDir, options.WithOwner("svc"), options.WithGroup("12"))
		SyncDir(syncDst, filepath.Join(base, "src", "*.conf"),
			options.WithOwner("syncer"), options.WithGroup("13"))
		EnsureDir(ensurePath,
			options.WithMode(0o750),
			options.WithOwner("creator"),
			options.WithGroup("14"),
		)
	})

	ops, err := RecordPlan("ownership", t.TempDir(), "ownership")
	if err != nil {
		t.Fatalf("RecordPlan: %v", err)
	}

	wantKinds := []plan.Kind{
		plan.KindPlan,
		plan.KindFile,
		plan.KindFile,
		plan.KindFile,
		plan.KindDir,
		plan.KindSyncDir,
		plan.KindEnsureDir,
	}
	if !reflect.DeepEqual(opsKinds(ops), wantKinds) {
		t.Fatalf("ops kinds = %v, want %v", opsKinds(ops), wantKinds)
	}

	// WithOwner/WithGroup must survive lowering...
	owned := ops[1]
	if owned.Owner != "daemon" || owned.Group != "1" {
		t.Fatalf("owned file op = %#v, want owner=daemon group=1", owned)
	}
	// ...while the unset file must stay free of ownership fields (build()'s
	// user.Current() default must never be pushed to remote hosts).
	unset := ops[2]
	if unset.Owner != "" || unset.Group != "" {
		t.Fatalf("unset file op = %#v, want no owner/group", unset)
	}
	// ...and an absent file carries no ownership (it is removed anyway).
	absent := ops[3]
	if !absent.Absent || absent.Owner != "" || absent.Group != "" {
		t.Fatalf("absent file op = %#v, want absent with no owner/group", absent)
	}
	dirOp := ops[4]
	if dirOp.Op != plan.KindDir || dirOp.Owner != "svc" || dirOp.Group != "12" {
		t.Fatalf("dir op = %#v, want owner=svc group=12", dirOp)
	}
	syncOp := ops[5]
	if syncOp.Owner != "syncer" || syncOp.Group != "13" {
		t.Fatalf("sync_dir op = %#v, want owner=syncer group=13", syncOp)
	}
	ensureOp := ops[6]
	if ensureOp.Owner != "creator" || ensureOp.Group != "14" || ensureOp.Mode != "0750" {
		t.Fatalf("ensure_dir op = %#v, want owner=creator group=14", ensureOp)
	}

	// Wire round-trip must preserve the ownership fields.
	raw, err := plan.EncodePlan(ops)
	if err != nil {
		t.Fatalf("EncodePlan: %v", err)
	}
	decoded, err := plan.DecodePlanBytes(raw)
	if err != nil {
		t.Fatalf("DecodePlanBytes: %v", err)
	}
	if !reflect.DeepEqual(decoded, ops) {
		t.Fatalf("round-trip mismatch\ngot  %#v\nwant %#v", decoded, ops)
	}
	if !strings.Contains(string(raw), `"owner":"daemon"`) ||
		!strings.Contains(string(raw), `"group":"1"`) {
		t.Fatalf("encoded plan lost ownership fields:\n%s", raw)
	}
}

func TestRecordPlanLowersNoCronAndNoService(t *testing.T) {
	ResetTasks()
	resource.ResetRepository()
	t.Cleanup(func() {
		resource.SetPlanDraftRecorder(nil)
		plan.SetRecording(false)
		plan.ResetRecord()
	})

	Task("absent_kinds", "", func() {
		NoCron("gone", options.WithCronUser("paul"))
		NoService("olddaemon")
	})

	ops, err := RecordPlan("absent", "", "absent_kinds")
	if err != nil {
		t.Fatalf("RecordPlan: %v", err)
	}
	if !reflect.DeepEqual(opsKinds(ops), []plan.Kind{plan.KindPlan, plan.KindCron, plan.KindService}) {
		t.Fatalf("ops kinds = %v", opsKinds(ops))
	}
	cronOp := ops[1]
	if cronOp.ID != "Cron[paul/gone]" || !cronOp.Absent || cronOp.CronUser != "paul" || cronOp.Command != "" {
		t.Fatalf("absent cron = %#v", cronOp)
	}
	svcOp := ops[2]
	if svcOp.Name != "olddaemon" || !svcOp.Absent {
		t.Fatalf("absent service = %#v", svcOp)
	}
}

func TestRecordPlanFailsOnUnrecordedResource(t *testing.T) {
	ResetTasks()
	resource.ResetRepository()
	t.Cleanup(func() {
		resource.SetPlanDraftRecorder(nil)
		plan.SetRecording(false)
		plan.ResetRecord()
	})

	Task("custom_kind", "", func() {
		_ = resource.Register("Widget", "gadget",
			resource.ApplierFunc(func() error { return nil }))
	})

	ops, err := RecordPlan("p", "", "custom_kind")
	if err == nil {
		t.Fatalf("expected loud error for registered resource without plan draft, got ops %v", opsKinds(ops))
	}
	if !strings.Contains(err.Error(), "Widget[gadget]") {
		t.Fatalf("error must name the unrecorded resource: %v", err)
	}
}

func opsKinds(ops []plan.Op) []plan.Kind {
	out := make([]plan.Kind, len(ops))
	for i, op := range ops {
		out[i] = op.Op
	}
	return out
}

// TestRecordPlanLowersTimerRestart pins the timer restart intent on the wire
// (task z12): WithRestart must survive record → wire → decode instead of
// being silently dropped from the recorded timer plan.
func TestRecordPlanLowersTimerRestart(t *testing.T) {
	ResetTasks()
	resource.ResetRepository()
	t.Cleanup(func() {
		resource.SetPlanDraftRecorder(nil)
		plan.SetRecording(false)
		plan.ResetRecord()
	})

	Task("timer_restart", "timer restart lowering", func() {
		Timer("fit.timer", options.WithUser, options.WithRestart)
	})

	ops, err := RecordPlan("timer_restart", t.TempDir(), "timer_restart")
	if err != nil {
		t.Fatalf("RecordPlan: %v", err)
	}
	wantKinds := []plan.Kind{plan.KindPlan, plan.KindTimer}
	if !reflect.DeepEqual(opsKinds(ops), wantKinds) {
		t.Fatalf("ops kinds = %v, want %v", opsKinds(ops), wantKinds)
	}
	op := ops[1]
	if !op.Restart || !op.User {
		t.Fatalf("timer op = %#v, want restart+user preserved", op)
	}

	// Wire round-trip.
	raw, err := plan.EncodePlan(ops)
	if err != nil {
		t.Fatalf("EncodePlan: %v", err)
	}
	decoded, err := plan.DecodePlanBytes(raw)
	if err != nil {
		t.Fatalf("DecodePlanBytes: %v", err)
	}
	if !reflect.DeepEqual(decoded, ops) {
		t.Fatalf("round-trip mismatch:\n got %#v\nwant %#v", decoded[1], ops[1])
	}
}

// TestRecordPlanLowersSystemdTimer pins declarative timer install fields on
// the wire (schema v7): command, calendar, and unit metadata must survive
// record → encode → decode.
func TestRecordPlanLowersSystemdTimer(t *testing.T) {
	ResetTasks()
	resource.ResetRepository()
	t.Cleanup(func() {
		resource.SetPlanDraftRecorder(nil)
		plan.SetRecording(false)
		plan.ResetRecord()
	})

	Task("systemd_timer_lower", "systemd timer lowering", func() {
		SystemdTimer("fit-job",
			options.WithCommand("/bin/true"),
			options.WithOnCalendar("*-*-* *:05:00"),
			options.WithOnBootSec("10min"),
			options.WithPersistent,
			options.WithDescription("fit timer"),
			options.WithServiceDescription("fit oneshot"),
			options.WithAfter("network-online.target"),
			options.WithWants("network-online.target"),
		)
	})

	ops, err := RecordPlan("systemd_timer_lower", t.TempDir(), "systemd_timer_lower")
	if err != nil {
		t.Fatalf("RecordPlan: %v", err)
	}
	wantKinds := []plan.Kind{plan.KindPlan, plan.KindSystemdTimer}
	if !reflect.DeepEqual(opsKinds(ops), wantKinds) {
		t.Fatalf("ops kinds = %v, want %v", opsKinds(ops), wantKinds)
	}
	op := ops[1]
	if op.Name != "fit-job" ||
		op.Command != "/bin/true" ||
		op.OnCalendar != "*-*-* *:05:00" ||
		op.OnBootSec != "10min" ||
		!op.Persistent ||
		op.Description != "fit timer" ||
		op.ServiceDescription != "fit oneshot" ||
		!reflect.DeepEqual(op.After, []string{"network-online.target"}) ||
		!reflect.DeepEqual(op.Wants, []string{"network-online.target"}) {
		t.Fatalf("systemd_timer op = %#v", op)
	}
	if op.ID != "SystemdTimer[fit-job]" {
		t.Fatalf("id = %q, want SystemdTimer[fit-job]", op.ID)
	}

	raw, err := plan.EncodePlan(ops)
	if err != nil {
		t.Fatalf("EncodePlan: %v", err)
	}
	decoded, err := plan.DecodePlanBytes(raw)
	if err != nil {
		t.Fatalf("DecodePlanBytes: %v", err)
	}
	if !reflect.DeepEqual(decoded, ops) {
		t.Fatalf("round-trip mismatch:\n got %#v\nwant %#v", decoded[1], ops[1])
	}
}

// TestRecordPlanLowersDependsOn pins the wire round-trip of dependency
// intent: DependsOn targets must reach plan.Op.Deps with their stable
// resource IDs so plan apply can order ops like the repository path does
// (task y12). A dep-free op must keep Deps nil so the field stays omitted.
func TestRecordPlanLowersDependsOn(t *testing.T) {
	ResetTasks()
	resource.ResetRepository()
	t.Cleanup(func() {
		resource.SetPlanDraftRecorder(nil)
		plan.SetRecording(false)
		plan.ResetRecord()
	})

	base := t.TempDir()
	first := filepath.Join(base, "first.conf")
	second := filepath.Join(base, "second.conf")

	Task("deps", "DependsOn lowering", func() {
		firstRes := File(first, options.WithContent("a"))
		File(second, options.WithContent("b"), options.DependsOn(firstRes))
	})

	ops, err := RecordPlan("deps", "", "deps")
	if err != nil {
		t.Fatalf("RecordPlan: %v", err)
	}
	wantKinds := []plan.Kind{plan.KindPlan, plan.KindFile, plan.KindFile}
	if !reflect.DeepEqual(opsKinds(ops), wantKinds) {
		t.Fatalf("ops kinds = %v, want %v", opsKinds(ops), wantKinds)
	}

	wantID := "File[" + first + "]"
	if ops[1].ID != wantID {
		t.Fatalf("dependency id = %q, want %q (IDs must stay stable for DependsOn)", ops[1].ID, wantID)
	}
	if got := ops[2].Deps; !reflect.DeepEqual(got, []string{wantID}) {
		t.Fatalf("dependent op deps = %#v, want [%s]", got, wantID)
	}
	// The dependency itself has none: the field must be omitted on the wire.
	if ops[1].Deps != nil {
		t.Fatalf("dep-free op deps = %#v, want nil", ops[1].Deps)
	}

	// Wire round-trip must preserve the dep list.
	raw, err := plan.EncodePlan(ops)
	if err != nil {
		t.Fatalf("EncodePlan: %v", err)
	}
	if !strings.Contains(string(raw), `"deps":["`+wantID+`"]`) {
		t.Fatalf("encoded plan lost deps field:\n%s", raw)
	}
	decoded, err := plan.DecodePlanBytes(raw)
	if err != nil {
		t.Fatalf("DecodePlanBytes: %v", err)
	}
	if !reflect.DeepEqual(decoded, ops) {
		t.Fatalf("round-trip mismatch\ngot  %#v\nwant %#v", decoded, ops)
	}
}

// TestRecordPlanLowersDaemonReloadDeps pins that the daemon_reload draft
// records its DependsOn targets: a gated daemon-reload must apply after the
// resources it watches, not just consult them.
func TestRecordPlanLowersDaemonReloadDeps(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("systemd resource is Linux-specific")
	}
	ResetTasks()
	resource.ResetRepository()
	t.Cleanup(func() {
		resource.SetPlanDraftRecorder(nil)
		plan.SetRecording(false)
		plan.ResetRecord()
	})

	conf := filepath.Join(t.TempDir(), "unit.conf")

	Task("reload_deps", "", func() {
		unit := File(conf, options.WithContent("x"))
		DaemonReload(options.WithUser, options.IfChanged, options.DependsOn(unit))
	})

	ops, err := RecordPlan("reload", "", "reload_deps")
	if err != nil {
		t.Fatalf("RecordPlan: %v", err)
	}
	if !reflect.DeepEqual(opsKinds(ops), []plan.Kind{plan.KindPlan, plan.KindFile, plan.KindDaemonReload}) {
		t.Fatalf("ops kinds = %v", opsKinds(ops))
	}
	wantID := "File[" + conf + "]"
	if got := ops[2].Deps; !reflect.DeepEqual(got, []string{wantID}) {
		t.Fatalf("daemon_reload deps = %#v, want [%s]", got, wantID)
	}
	if !reflect.DeepEqual(ops[2].Watch, []string{wantID}) {
		t.Fatalf("daemon_reload watch = %#v, want [%s]", ops[2].Watch, wantID)
	}
}

// TestRecordPlanLowersSyncDirSourceDir pins the sync_dir source_dir wire
// field (schema v6): the recipe's declared source directory must travel on
// the op so destination apply renders tree .tmpl files' {{.Param}} from a
// stable identity instead of the ephemeral blob-extraction path (which
// changes every plan run and flaps the rendered checksums). For the glob
// flavor the declared source directory is the glob pattern's directory.
// A plain dir op and file ops must carry no source_dir.
func TestRecordPlanLowersSyncDirSourceDir(t *testing.T) {
	ResetTasks()
	resource.ResetRepository()
	t.Cleanup(func() {
		resource.SetPlanDraftRecorder(nil)
		plan.SetRecording(false)
		plan.ResetRecord()
	})

	base := t.TempDir()
	treeSrc := filepath.Join(base, "tree")
	globSrc := filepath.Join(base, "src")
	// Record-time packaging walks the sources: both must exist with content.
	for _, dir := range []string{treeSrc, globSrc} {
		if err := os.Mkdir(dir, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "app.conf"), []byte("x\n"), 0o640); err != nil {
			t.Fatal(err)
		}
	}

	Task("syncdir_source", "sync_dir source_dir lowering", func() {
		Dir(filepath.Join(base, "plain"))
		Dir(filepath.Join(base, "tree-dst"), options.WithSource(treeSrc))
		SyncDir(filepath.Join(base, "glob-dst"), filepath.Join(globSrc, "*.conf"))
		File(filepath.Join(base, "f.conf"), options.WithContent("x"))
	})

	ops, err := RecordPlan("syncdir_src", t.TempDir(), "syncdir_source")
	if err != nil {
		t.Fatalf("RecordPlan: %v", err)
	}
	wantKinds := []plan.Kind{
		plan.KindPlan,
		plan.KindDir,
		plan.KindSyncDir,
		plan.KindSyncDir,
		plan.KindFile,
	}
	if !reflect.DeepEqual(opsKinds(ops), wantKinds) {
		t.Fatalf("ops kinds = %v, want %v", opsKinds(ops), wantKinds)
	}

	// A plain dir op carries no source_dir.
	if ops[1].SourceDir != "" {
		t.Fatalf("dir op source_dir = %q, want empty", ops[1].SourceDir)
	}
	// The tree flavor carries the declared source directory verbatim.
	if got, want := ops[2].SourceDir, treeSrc; got != want {
		t.Fatalf("tree sync_dir source_dir = %q, want %q", got, want)
	}
	// The glob flavor carries the glob pattern's directory.
	if got, want := ops[3].SourceDir, globSrc; got != want {
		t.Fatalf("glob sync_dir source_dir = %q, want %q", got, want)
	}
	// File ops carry no source_dir.
	if ops[4].SourceDir != "" {
		t.Fatalf("file op source_dir = %q, want empty", ops[4].SourceDir)
	}

	// The field must survive the wire round-trip under its json tag.
	raw, err := plan.EncodePlan(ops)
	if err != nil {
		t.Fatalf("EncodePlan: %v", err)
	}
	if !strings.Contains(string(raw), `"source_dir":"`+treeSrc+`"`) {
		t.Fatalf("encoded plan lost source_dir field:\n%s", raw)
	}
	decoded, err := plan.DecodePlanBytes(raw)
	if err != nil {
		t.Fatalf("DecodePlanBytes: %v", err)
	}
	if !reflect.DeepEqual(decoded, ops) {
		t.Fatalf("round-trip mismatch\ngot  %#v\nwant %#v", decoded, ops)
	}
}

// WhenProfile with more than one profile must lower to a single serializable
// OR predicate (Fact: "profile", In: profiles) instead of being marked
// opaque — h5 regression: it used to fall back to c.opaqueWhen, which
// planWhenForCandidate then dropped from the recorded plan entirely.
func TestRecordPlanWhenProfileMultiLowersToInPredicate(t *testing.T) {
	ResetTasks()
	resource.ResetRepository()
	t.Cleanup(func() {
		resource.SetPlanDraftRecorder(nil)
		plan.SetRecording(false)
		plan.ResetRecord()
	})

	Activate(Facts{GOOS: "linux", Profile: "fedora"})

	Task("multi_profile", "", func() {
		Package("fish")
	}, WhenProfile("fedora", "rocky"))

	ops, err := RecordPlan("multi-profile", "", "multi_profile")
	if err != nil {
		t.Fatalf("RecordPlan: %v", err)
	}
	if len(ops) != 4 || ops[1].Op != plan.KindWhenBegin {
		t.Fatalf("ops = %#v", ops)
	}
	begin := ops[1]
	if len(begin.All) != 1 {
		t.Fatalf("when_begin predicates = %#v, want exactly 1", begin.All)
	}
	want := plan.Predicate{Fact: "profile", In: []string{"fedora", "rocky"}}
	if !reflect.DeepEqual(begin.All[0], want) {
		t.Fatalf("predicate = %#v, want %#v (WhenProfile must not fall back to opaque)", begin.All[0], want)
	}
}

// A task built with WhenLinux() (serializable) AND a custom When(fn)
// (opaque) must still ship the goos guard in the recorded plan: the opaque
// predicate is only an extra controller-side filter, never a reason to drop
// the serializable one. This is the exact h5 regression scenario — before
// the fix, planWhenForCandidate returned nil here because c.opaqueWhen was
// true, silently dropping the OS guard.
func TestRecordPlanOpaqueWhenKeepsSerializableGuard(t *testing.T) {
	if runtime.GOOS != "linux" {
		// WhenLinux's own injected controller-side check needs a real
		// linux host to pass; see the opaque-filter comment below.
		t.Skip("WhenLinux's controller-side check requires a linux host")
	}
	ResetTasks()
	resource.ResetRepository()
	t.Cleanup(func() {
		resource.SetPlanDraftRecorder(nil)
		plan.SetRecording(false)
		plan.ResetRecord()
	})

	// The opaque predicate below is evaluated against the REAL controller
	// facts (DetectFacts), not any Activate override, so it must be
	// trivially true here — the point of this test is the serializable
	// goos guard surviving, not the opaque controller-side check itself
	// (that is covered by TestRecordPlanOpaqueWhenRequiresLocalPass /
	// TestRecordPlanOpaqueWhenFailingControllerFilterErrors).
	Task("linux_plus_custom", "", func() {
		Package("fish")
	}, WhenLinux(), When(func(f Facts) bool { return true }))

	ops, err := RecordPlan("linux-plus-custom", "", "linux_plus_custom")
	if err != nil {
		t.Fatalf("RecordPlan: %v", err)
	}
	if len(ops) != 4 || ops[1].Op != plan.KindWhenBegin {
		t.Fatalf("ops = %#v; want a when_begin carrying the goos guard", ops)
	}
	begin := ops[1]
	if len(begin.All) != 1 || !reflect.DeepEqual(begin.All[0], plan.Predicate{Fact: "goos", Eq: "linux"}) {
		t.Fatalf("when_begin predicates = %#v, want [{goos linux}]", begin.All)
	}
}

// When the opaque controller-side filter itself fails, recording must still
// error out (no guard to ship, and the task should not have activated here
// at all) — this is unchanged behavior, kept as a guard against a future
// regression while fixing the drop-the-guard bug above.
func TestRecordPlanOpaqueWhenFailingControllerFilterErrors(t *testing.T) {
	ResetTasks()
	resource.ResetRepository()
	t.Cleanup(func() {
		resource.SetPlanDraftRecorder(nil)
		plan.SetRecording(false)
		plan.ResetRecord()
	})

	Activate(Facts{GOOS: "linux", Profile: "fedora"})

	Task("linux_plus_failing_custom", "", func() {
		Package("fish")
	}, WhenLinux(), When(func(f Facts) bool { return false }))

	if _, err := RecordPlan("linux-plus-failing-custom", "", "linux_plus_failing_custom"); err == nil {
		t.Fatal("RecordPlan: want error when the opaque predicate fails on the controller")
	}
}
