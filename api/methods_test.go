package api

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
)

func TestActivateWhenFilters(t *testing.T) {
	ResetTasks()

	Task("always", "a", func() {})
	Task("linux_only", "l", func() {}, WhenLinux())
	Task("fedora_only", "f", func() {}, WhenProfile("fedora"))
	Task("rocky_host", "r", func() {}, WhenHostnameContains("rocky"))

	Activate(Facts{Profile: "fedora", GOOS: "linux", Hostname: "earth"})
	got := taskNames(t)
	want := []string{"always", "fedora_only", "linux_only"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("fedora/linux = %v, want %v", got, want)
	}

	Activate(Facts{Profile: "rocky", GOOS: "darwin", Hostname: "rocky-box"})
	got = taskNames(t)
	want = []string{"always", "rocky_host"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("rocky/darwin = %v, want %v", got, want)
	}
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

	Activate(Facts{Profile: "rocky"})
	if got := Matching("^pkg_"); len(got) != 0 {
		t.Fatalf("rocky should skip pkg: %v", got)
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

	// Both tasks queued as candidates, but only demo_ping is active on a
	// host whose name does not contain "rocky": the serializable
	// WhenHostnameContains option gates activation locally.
	Activate(Facts{Hostname: "earth"})
	got := taskNames(t)
	want := []string{"demo_ping"}
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
		begin.All[0] != (plan.Predicate{Fact: "hostname_contains", Eq: "rocky"}) {
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
	if len(ops[1].All) != 1 || ops[1].All[0] != (plan.Predicate{Fact: "goos", Eq: "linux"}) {
		t.Fatalf("group when lowered = %#v", ops[1].All)
	}
	if !ops[1].Elevate || !ops[2].Elevate {
		t.Fatalf("group When + method Opts must compose; got %#v", ops)
	}
}

func TestRegisterMethodsOptsBadSignaturePanics(t *testing.T) {
	ResetTasks()

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic for OptsX companion with wrong signature")
		}
		msg, ok := r.(string)
		if !ok || !strings.Contains(msg, "OptsBroken must be func() TaskOptions") {
			t.Fatalf("unexpected panic value: %v", r)
		}
	}()
	RegisterMethods(badOpts{}, WithPrefix("demo_"))
}

// OptsPing is valid; OptsBroken has a wrong signature for its Broken task.
type badOpts struct{}

func (badOpts) OptsPing() TaskOptions { return nil }
func (badOpts) Ping()                 {}
func (badOpts) OptsBroken() string    { return "wrong return type" }
func (badOpts) Broken()               {}

// OptsDemo composes with a group-wide When option.
type optsComposed struct{ dir string }

func (o optsComposed) OptsDemo() TaskOptions { return TaskOptions{Privileged()} }

func (o optsComposed) Demo() {
	File(filepath.Join(o.dir, "composed.txt"), options.WithContent("x"))
}
