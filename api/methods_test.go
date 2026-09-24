package api

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/internal/declerr"
	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
)

// TestActivateWhenFilters pins task 8h2's activation rule: only an opaque
// When predicate hides a task; a serializable guard that does not hold for
// the activation facts marks the task destination-guarded instead.
func TestActivateWhenFilters(t *testing.T) {
	ResetTasks()

	Task("always", "a", func() {})
	Task("linux_only", "l", func() {}, WhenLinux())
	Task("fedora_only", "f", func() {}, WhenProfile("fedora"))
	Task("rocky_host", "r", func() {}, WhenHostnameContains("rocky"))
	Task("opaque_linux", "o", func() {}, When(func(f Facts) bool { return f.GOOS == "linux" }))

	Activate(Facts{Profile: "fedora", GOOS: "linux", Hostname: "earth"})
	want := map[string]string{"always": "", "fedora_only": "", "linux_only": "",
		"opaque_linux": "", "rocky_host": "hostname_contains=rocky"}
	if got := taskGuards(t); !reflect.DeepEqual(got, want) {
		t.Fatalf("fedora/linux = %v, want %v", got, want)
	}

	Activate(Facts{Profile: "rocky", GOOS: "darwin", Hostname: "rocky-box"})
	want = map[string]string{"always": "", "fedora_only": "profile=fedora",
		"linux_only": "goos=linux", "rocky_host": ""}
	if got := taskGuards(t); !reflect.DeepEqual(got, want) {
		t.Fatalf("rocky/darwin = %v, want %v", got, want)
	}
}

// taskGuards maps every listed task to its DestinationGuard mark.
func taskGuards(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, info := range Tasks() {
		out[info.Name] = info.DestinationGuard
	}
	return out
}

func taskNames(t *testing.T) []string {
	t.Helper()
	infos := Tasks()
	names := make([]string, len(infos))
	for i, info := range infos {
		names[i] = info.Name
	}
	return names
}

func TestRegisterMethods(t *testing.T) {
	ResetTasks()
	RegisterMethods(reflectHome{}, WithPrefix("home_"))
	Activate(Facts{GOOS: "linux", Profile: "fedora"})

	got := Matching("^home_")
	want := []string{"home_helix", "home_hexai"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Matching = %v, want %v", got, want)
	}

	infos := Tasks()
	var helixDesc string
	for _, info := range infos {
		if info.Name == "home_helix" {
			helixDesc = info.Description
		}
	}
	if helixDesc != "Install helix" {
		t.Fatalf("helix desc = %q", helixDesc)
	}

	Activate(Facts{GOOS: "darwin", Profile: "fedora"})
	got = Matching("^home_")
	want = []string{"home_helix"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("darwin Matching = %v, want %v", got, want)
	}
}

type reflectHome struct{}

func (reflectHome) Helix()            {}
func (reflectHome) DescHelix() string { return "Install helix" }

func (reflectHome) Hexai()                 {}
func (reflectHome) DescHexai() string      { return "Install hexai" }
func (reflectHome) WhenHexai(f Facts) bool { return f.GOOS == "linux" }

func TestRegisterMethodsGroupWhen(t *testing.T) {
	ResetTasks()
	RegisterMethods(reflectPkg{}, WithPrefix("pkg_"), WithGroupWhen(WhenProfile("fedora")))

	Activate(Facts{Profile: "fedora"})
	if got := Matching("^pkg_"); !reflect.DeepEqual(got, []string{"pkg_fedora"}) {
		t.Fatalf("fedora: %v", got)
	}

	// Task 8h2: a WithGroupWhen(WhenProfile) guard travels to the
	// destination, so a controller of another profile still matches the
	// task (a pattern aggregate pushed from it records the member) and
	// lists it as destination-guarded.
	Activate(Facts{Profile: "rocky"})
	if got := Matching("^pkg_"); !reflect.DeepEqual(got, []string{"pkg_fedora"}) {
		t.Fatalf("rocky must still match the destination-guarded task: %v", got)
	}
	if got := taskGuards(t)["pkg_fedora"]; got != "profile=fedora" {
		t.Fatalf("rocky: DestinationGuard = %q, want profile=fedora", got)
	}
}

type reflectPkg struct{}

func (reflectPkg) Fedora()            {}
func (reflectPkg) DescFedora() string { return "Fedora packages" }

func TestCamelToSnake(t *testing.T) {
	cases := map[string]string{
		"Helix":           "helix",
		"TmuxRocky":       "tmux_rocky",
		"FishCompletions": "fish_completions",
		"Ssh":             "ssh",
		"SystemdUser":     "systemd_user",
	}
	for in, want := range cases {
		if got := camelToSnake(in); got != want {
			t.Errorf("camelToSnake(%q) = %q, want %q", in, got, want)
		}
	}
}

// OptsX companion demo: per-method TaskOptions via the Desc/When-style
// companion convention.
type optsCompanion struct{ dir string }

func (o optsCompanion) DescPing() string { return "demo privileged ping" }

func (o optsCompanion) OptsPing() TaskOptions { return TaskOptions{Privileged()} }

func (o optsCompanion) Ping() {
	File(filepath.Join(o.dir, "ping.txt"), options.WithContent("x"))
}

func (o optsCompanion) OptsRockyCron() []TaskOption {
	return []TaskOption{Privileged(), WhenHostnameContains("rocky")}
}

func (o optsCompanion) RockyCron() {
	File(filepath.Join(o.dir, "cron.txt"), options.WithContent("x"))
}

func TestRegisterMethodsOptsCompanion(t *testing.T) {
	ResetTasks()
	resource.ResetRepository()
	t.Cleanup(func() {
		resource.SetPlanDraftRecorder(nil)
		plan.SetRecording(false)
		plan.ResetRecord()
	})

	RegisterMethods(optsCompanion{dir: t.TempDir()}, WithPrefix("demo_"))

	// Both tasks are active on a host whose name does not contain "rocky":
	// the serializable WhenHostnameContains option only marks demo_rocky_cron
	// destination-guarded there (task 8h2); it travels as a when_begin.
	Activate(Facts{Hostname: "earth"})
	got := taskGuards(t)
	want := map[string]string{"demo_ping": "", "demo_rocky_cron": "hostname_contains=rocky"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("tasks on earth = %v, want %v", got, want)
	}
	for _, info := range Tasks() {
		if info.Name == "demo_ping" && info.Description != "demo privileged ping" {
			t.Fatalf("DescPing companion ignored: %q", info.Description)
		}
	}

	// Opts* methods must not become tasks themselves.
	for _, info := range Tasks() {
		if strings.Contains(info.Name, "opts_") {
			t.Fatalf("Opts companion leaked as task %q", info.Name)
		}
	}

	// demo_ping is privileged: its recorded ops carry elevate=true.
	ops, err := RecordPlan("opts-ping", "", "demo_ping")
	if err != nil {
		t.Fatalf("RecordPlan demo_ping: %v", err)
	}
	if len(ops) != 2 || ops[0].Op != plan.KindPlan || ops[1].Op != plan.KindFile {
		t.Fatalf("demo_ping ops = %#v", ops)
	}
	if !ops[1].Elevate {
		t.Fatalf("demo_ping file op not elevated: %#v", ops[1])
	}

	// Recording the gated task by name still works (candidates are
	// registration-listed, not activation-listed) and lowers the
	// serializable hostname predicate wrapped around the elevated op.
	ops, err = RecordPlan("opts-rocky", "", "demo_rocky_cron")
	if err != nil {
		t.Fatalf("RecordPlan demo_rocky_cron: %v", err)
	}
	if len(ops) != 4 || ops[0].Op != plan.KindPlan ||
		ops[1].Op != plan.KindWhenBegin || ops[3].Op != plan.KindWhenEnd {
		t.Fatalf("rocky ops = %#v", ops)
	}
	begin := ops[1]
	if begin.All == nil || len(begin.All) != 1 ||
		!reflect.DeepEqual(begin.All[0], plan.Predicate{Fact: "hostname_contains", Eq: "rocky"}) {
		t.Fatalf("when_begin predicates = %#v", begin.All)
	}
	if !begin.Elevate || !ops[2].Elevate {
		t.Fatalf("when_begin/op not elevated: %#v / %#v", begin, ops[2])
	}
}

func TestRegisterMethodsOptsComposesWithGroupWhen(t *testing.T) {
	ResetTasks()
	resource.ResetRepository()
	t.Cleanup(func() {
		resource.SetPlanDraftRecorder(nil)
		plan.SetRecording(false)
		plan.ResetRecord()
	})

	RegisterMethods(optsComposed{dir: t.TempDir()}, WithPrefix("demo_"), WithGroupWhen(WhenLinux()))

	ops, err := RecordPlan("opts-composed", "", "demo_demo")
	if err != nil {
		t.Fatalf("RecordPlan: %v", err)
	}
	// header + when_begin(goos=linux, elevate) + file(elevate) + when_end
	if len(ops) != 4 || ops[1].Op != plan.KindWhenBegin || ops[2].Op != plan.KindFile {
		t.Fatalf("ops = %#v", ops)
	}
	if len(ops[1].All) != 1 || !reflect.DeepEqual(ops[1].All[0], plan.Predicate{Fact: "goos", Eq: "linux"}) {
		t.Fatalf("group when lowered = %#v", ops[1].All)
	}
	if !ops[1].Elevate || !ops[2].Elevate {
		t.Fatalf("group When + method Opts must compose; got %#v", ops)
	}
}

// TestRegisterMethodsOptsBadSignatureIsDeclarationError: a wrong OptsX
// signature is a declaration error and skips only that method's task (it
// must not register with the companion silently dropped); the valid sibling
// still registers.
func TestRegisterMethodsOptsBadSignatureIsDeclarationError(t *testing.T) {
	requireDeclErr(t, "RegisterMethods: OptsBroken must be func() TaskOptions", func() {
		RegisterMethods(badOpts{}, WithPrefix("demo_"))
	})
	requireQueued(t, "demo_ping")
	requireNotQueued(t, "demo_broken")
}

// requireQueued fails unless a task candidate named name is queued.
func requireQueued(t *testing.T, name string) {
	t.Helper()
	if _, ok := findCandidate(name); !ok {
		t.Fatalf("task %q is not queued", name)
	}
}

// requireNotQueued fails when a task candidate named name is queued.
func requireNotQueued(t *testing.T, name string) {
	t.Helper()
	if _, ok := findCandidate(name); ok {
		t.Fatalf("task %q is queued, want it refused", name)
	}
}

// OptsPing is valid; OptsBroken has a wrong signature for its Broken task.
type badOpts struct{}

func (badOpts) OptsPing() TaskOptions { return nil }
func (badOpts) Ping()                 {}
func (badOpts) OptsBroken() string    { return "wrong return type" }
func (badOpts) Broken()               {}

// TestRegisterMethodsWhenBadSignatureIsDeclarationError: a wrong WhenX
// signature is a declaration error and Broken is not registered, so it can
// never run unguarded; the valid sibling still registers.
func TestRegisterMethodsWhenBadSignatureIsDeclarationError(t *testing.T) {
	requireDeclErr(t, "RegisterMethods: WhenBroken must be func(Facts) bool", func() {
		RegisterMethods(badWhen{}, WithPrefix("demo_"))
	})
	requireQueued(t, "demo_ping")
	requireNotQueued(t, "demo_broken")
}

// WhenPing is valid; WhenBroken has a wrong signature for its Broken task
// (wrong param type), which must refuse the task rather than silently drop
// the guard and let Broken run on every host.
type badWhen struct{}

func (badWhen) WhenPing(f Facts) bool { return f.GOOS == "linux" }
func (badWhen) Ping()                 {}
func (badWhen) WhenBroken(s string) bool {
	return s == "linux"
}
func (badWhen) Broken() {}

// twoBrokenWhens has two methods with wrong WhenX companion signatures
// (mirroring gonf task gc2's review: WhenAlpha() string / WhenBeta(int, int)
// bool). Before gc2, registerMethodTasks ranged a map of method names, whose
// iteration order Go randomizes per range, so which of the two errors
// surfaced (and stuck, since internal/declerr is first-error-wins) varied
// run to run. It now iterates a slice built from rt.Method(i), which
// reflect.Type.Method documents as sorted in lexicographic order, so "Alpha"
// is always seen (and reported) before "Beta".
type twoBrokenWhens struct{}

func (twoBrokenWhens) Alpha()            {}
func (twoBrokenWhens) WhenAlpha() string { return "wrong" }

func (twoBrokenWhens) Beta()                  {}
func (twoBrokenWhens) WhenBeta(int, int) bool { return false }

// TestRegisterMethodsCompanionErrorOrderIsDeterministic pins gc2: a recipe
// with several broken companions always reports the alphabetically-first
// method's error, run after run, instead of whichever the map iteration
// happened to visit first (see twoBrokenWhens). It repeats registration many
// times in-process — Go re-randomizes a map's iteration start on every
// range, not just once per process, so the old bug would already show up
// as variance within this loop.
func TestRegisterMethodsCompanionErrorOrderIsDeterministic(t *testing.T) {
	const n = 200
	for i := 0; i < n; i++ {
		ResetForTest()
		ResetInventory()
		RegisterMethods(twoBrokenWhens{}, WithPrefix("probe_"))
		err := declerr.First()
		if err == nil {
			t.Fatalf("iter %d: expected a declaration error, got nil", i)
		}
		if want := "RegisterMethods: WhenAlpha must be func(Facts) bool"; err.Error() != want {
			t.Fatalf("iter %d: error = %q, want %q (alphabetically-first method)", i, err.Error(), want)
		}
	}
	ResetForTest()
	ResetInventory()
}

func TestRegisterMethodsWhenGoodSignatureGuards(t *testing.T) {
	ResetTasks()
	RegisterMethods(reflectWhenOK{}, WithPrefix("demo_"))

	Activate(Facts{GOOS: "linux"})
	got := taskNames(t)
	want := []string{"demo_guarded"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("linux: %v, want %v", got, want)
	}

	Activate(Facts{GOOS: "darwin"})
	got = taskNames(t)
	if len(got) != 0 {
		t.Fatalf("darwin should skip guarded task: %v", got)
	}
}

type reflectWhenOK struct{}

func (reflectWhenOK) WhenGuarded(f Facts) bool { return f.GOOS == "linux" }
func (reflectWhenOK) Guarded()                 {}

// OptsDemo composes with a group-wide When option.
type optsComposed struct{ dir string }

func (o optsComposed) OptsDemo() TaskOptions { return TaskOptions{Privileged()} }

func (o optsComposed) Demo() {
	File(filepath.Join(o.dir, "composed.txt"), options.WithContent("x"))
}

// Struct-level Opts() default companion: one Privileged() for the whole
// struct, with a method-level opt-out for the unprivileged smoke test.
type structOpts struct{ dir string }

func (o structOpts) Opts() TaskOptions { return TaskOptions{Privileged()} }

func (o structOpts) DescEverything() string { return "everything privileged" }

func (o structOpts) Everything() {
	File(filepath.Join(o.dir, "everything.txt"), options.WithContent("x"))
}

func (o structOpts) DescSmoke() string { return "unprivileged smoke test" }

// OptsSmoke opts OUT of the struct-level default: empty TaskOptions.
func (o structOpts) OptsSmoke() TaskOptions { return TaskOptions{} }

func (o structOpts) Smoke() {
	File(filepath.Join(o.dir, "smoke.txt"), options.WithContent("x"))
}

func TestRegisterMethodsStructOptsDefault(t *testing.T) {
	ResetTasks()
	resource.ResetRepository()
	t.Cleanup(func() {
		resource.SetPlanDraftRecorder(nil)
		plan.SetRecording(false)
		plan.ResetRecord()
	})

	dir := t.TempDir()
	RegisterMethods(structOpts{dir: dir}, WithPrefix("demo_"))

	// The exact "Opts" companion must not register as a task.
	for _, info := range Tasks() {
		if info.Name == "opts" {
			t.Fatal("struct-level Opts companion leaked as task")
		}
	}

	ops, err := RecordPlan("struct-opts", "", "demo_everything")
	if err != nil {
		t.Fatalf("RecordPlan demo_everything: %v", err)
	}
	if len(ops) != 2 || ops[0].Op != plan.KindPlan || ops[1].Op != plan.KindFile {
		t.Fatalf("demo_everything ops = %#v", ops)
	}
	if !ops[1].Elevate {
		t.Fatalf("struct-level default not applied: %#v", ops[1])
	}

	// The opt-out method records WITHOUT elevation.
	ops, err = RecordPlan("struct-opts-smoke", "", "demo_smoke")
	if err != nil {
		t.Fatalf("RecordPlan demo_smoke: %v", err)
	}
	if len(ops) != 2 || ops[1].Op != plan.KindFile {
		t.Fatalf("demo_smoke ops = %v", opsKinds(ops))
	}
	if ops[1].Elevate {
		t.Fatalf("OptsX empty must replace (not compose) the struct default: %#v", ops[1])
	}
}

func TestRegisterMethodsStructOptsReplacedByOptsX(t *testing.T) {
	ResetTasks()
	resource.ResetRepository()
	t.Cleanup(func() {
		resource.SetPlanDraftRecorder(nil)
		plan.SetRecording(false)
		plan.ResetRecord()
	})

	// OptsRocky REPLACES the struct default: the recorded op is gated by the
	// hostname recipe and NOT elevated.
	Activate(Facts{Hostname: "earth"})
	RegisterMethods(gated{dir: t.TempDir()}, WithPrefix("demo_"))

	ops, err := RecordPlan("replaced", "", "demo_rocky")
	if err != nil {
		t.Fatalf("RecordPlan: %v", err)
	}
	if len(ops) != 4 || ops[1].Op != plan.KindWhenBegin || ops[2].Op != plan.KindFile {
		t.Fatalf("ops = %v", opsKinds(ops))
	}
	if !reflect.DeepEqual(ops[1].All[0], plan.Predicate{Fact: "hostname_contains", Eq: "rocky"}) {
		t.Fatalf("when predicates = %#v", ops[1].All)
	}
	if ops[2].Elevate {
		t.Fatalf("method OptsX must REPLACE (not compose) the struct default: %#v", ops[2])
	}
}

// TestRegisterMethodsStructOptsBadSignatureIsDeclarationError: a wrong
// struct-level Opts registers no task of the struct (the default could have
// been Privileged()).
func TestRegisterMethodsStructOptsBadSignatureIsDeclarationError(t *testing.T) {
	requireDeclErr(t, "RegisterMethods: Opts must be func() TaskOptions", func() {
		RegisterMethods(badStruct{}, WithPrefix("demo_"))
	})
	requireNotQueued(t, "demo_ping")
}

// TestRegisterMethodsBadReceiverIsDeclarationError: a nil pointer or a
// non-struct receiver is a declaration error, not a panic.
func TestRegisterMethodsBadReceiverIsDeclarationError(t *testing.T) {
	requireDeclErr(t, "RegisterMethods: nil pointer", func() { RegisterMethods((*badStruct)(nil)) })
	requireDeclErr(t, "RegisterMethods: want struct or *struct, got int", func() { RegisterMethods(42) })
}

// Opts with a wrong signature is the struct-level default companion.
type badStruct struct{}

func (badStruct) Opts() string { return "wrong" }
func (badStruct) Ping()        {}

// OptsRocky REPLACES the struct default: gated by the hostname recipe and
// NOT privileged.
type gated struct{ dir string }

func (gated) Opts() TaskOptions { return TaskOptions{Privileged()} }
func (gated) OptsRocky() TaskOptions {
	return TaskOptions{WhenHostnameContains("rocky")}
}
func (g gated) Rocky() {
	File(filepath.Join(g.dir, "rocky.txt"), options.WithContent("x"))
}

// WithGroupWhen composes with the struct-level Opts() default: both apply.
type groupAndStructOpts struct{ dir string }

func (g groupAndStructOpts) Opts() TaskOptions { return TaskOptions{Privileged()} }

func (g groupAndStructOpts) Demo() {
	File(filepath.Join(g.dir, "demo.txt"), options.WithContent("x"))
}

func TestRegisterMethodsGroupWhenComposesWithStructOpts(t *testing.T) {
	ResetTasks()
	resource.ResetRepository()
	t.Cleanup(func() {
		resource.SetPlanDraftRecorder(nil)
		plan.SetRecording(false)
		plan.ResetRecord()
	})

	RegisterMethods(groupAndStructOpts{dir: t.TempDir()},
		WithPrefix("demo_"), WithGroupWhen(WhenLinux()))

	ops, err := RecordPlan("group-struct", "", "demo_demo")
	if err != nil {
		t.Fatalf("RecordPlan: %v", err)
	}
	// header + when_begin(goos=linux, elevate) + file(elevate) + when_end
	if len(ops) != 4 || ops[1].Op != plan.KindWhenBegin || ops[2].Op != plan.KindFile {
		t.Fatalf("ops = %v", opsKinds(ops))
	}
	if len(ops[1].All) != 1 || !reflect.DeepEqual(ops[1].All[0], plan.Predicate{Fact: "goos", Eq: "linux"}) {
		t.Fatalf("group when lowered = %#v", ops[1].All)
	}
	if !ops[1].Elevate || !ops[2].Elevate {
		t.Fatalf("WithGroupWhen + struct Opts must compose: %#v", ops)
	}
}
