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

// The fleet shape: the execution contract declared ON the struct itself.
type embeddedMarker struct {
	RequiresRoot
	dir string
}

func (embeddedMarker) DescCron() string { return "cron schedule" }

func (e embeddedMarker) Cron() {
	File(filepath.Join(e.dir, "cron.txt"), options.WithContent("x"))
}

func TestRegisterMethodsEmbeddedRequiresRoot(t *testing.T) {
	ResetTasks()
	resource.ResetRepository()
	t.Cleanup(func() {
		resource.SetPlanDraftRecorder(nil)
		plan.SetRecording(false)
		plan.ResetRecord()
	})

	RegisterMethods(embeddedMarker{dir: t.TempDir()}, WithPrefix("demo_"))

	ops, err := RecordPlan("marker", "", "demo_cron")
	if err != nil {
		t.Fatalf("RecordPlan: %v", err)
	}
	if len(ops) != 2 || ops[0].Op != plan.KindPlan || ops[1].Op != plan.KindFile {
		t.Fatalf("ops = %v", opsKinds(ops))
	}
	if !ops[1].Elevate {
		t.Fatalf("embedded RequiresRoot not applied: %#v", ops[1])
	}
}

// A method's own OptsX companion adds to the embedded marker, so
// Unprivileged() is the opt-out that wins over RequiresRoot, while an OptsX
// that only adds Needs keeps it.
type markerWithOptOut struct {
	RequiresRoot
	dir string
}

func (markerWithOptOut) OptsSmoke() TaskOptions { return TaskOptions{Unprivileged()} }

func (markerWithOptOut) OptsNeedy() TaskOptions { return TaskOptions{Needs("smoke")} }

func (m markerWithOptOut) Needy() {
	File(filepath.Join(m.dir, "needy.txt"), options.WithContent("x"))
}

func (m markerWithOptOut) Smoke() {
	File(filepath.Join(m.dir, "smoke.txt"), options.WithContent("x"))
}

func TestRegisterMethodsMarkerOptsXOptOut(t *testing.T) {
	ResetTasks()
	resource.ResetRepository()
	t.Cleanup(func() {
		resource.SetPlanDraftRecorder(nil)
		plan.SetRecording(false)
		plan.ResetRecord()
	})

	RegisterMethods(markerWithOptOut{dir: t.TempDir()}, WithPrefix("demo_"))

	ops, err := RecordPlan("optout", "", "demo_smoke")
	if err != nil {
		t.Fatalf("RecordPlan: %v", err)
	}
	if len(ops) != 2 || ops[1].Op != plan.KindFile {
		t.Fatalf("ops = %v", opsKinds(ops))
	}
	if ops[1].Elevate {
		t.Fatalf("Unprivileged in OptsX must win over the embedded marker: %#v", ops[1])
	}

	ops, err = RecordPlan("needy", "", "demo_needy")
	if err != nil {
		t.Fatalf("RecordPlan demo_needy: %v", err)
	}
	var needy *plan.Op
	for i := range ops {
		if ops[i].Op == plan.KindFile && strings.HasSuffix(ops[i].Path, "needy.txt") {
			needy = &ops[i]
		}
	}
	if needy == nil || !needy.Elevate {
		t.Fatalf("OptsX adding Needs must keep RequiresRoot: %v", opsKinds(ops))
	}
}

// The embedded marker composes with the Opts() companion.
type markerAndOpts struct{ dir string }

func (markerAndOpts) StructTaskOptions() TaskOptions {
	return TaskOptions{Privileged()}
}

func (markerAndOpts) Opts() TaskOptions {
	return TaskOptions{WhenHostnameContains("rocky")}
}

func (m markerAndOpts) Demo() {
	File(filepath.Join(m.dir, "demo.txt"), options.WithContent("x"))
}

func TestRegisterMethodsMarkerComposesWithOpts(t *testing.T) {
	ResetTasks()
	resource.ResetRepository()
	t.Cleanup(func() {
		resource.SetPlanDraftRecorder(nil)
		plan.SetRecording(false)
		plan.ResetRecord()
	})

	Activate(Facts{Hostname: "rocky-box"})
	RegisterMethods(markerAndOpts{dir: t.TempDir()}, WithPrefix("demo_"))

	// The Opts() companion COMPOSES with the embedded marker: the recorded
	// op is elevated (marker) AND gated by the hostname recipe (Opts()).
	ops, err := RecordPlan("marker-opts", "", "demo_demo")
	if err != nil {
		t.Fatalf("RecordPlan: %v", err)
	}
	if len(ops) != 4 || ops[1].Op != plan.KindWhenBegin || ops[2].Op != plan.KindFile {
		t.Fatalf("ops = %v", opsKinds(ops))
	}
	if !reflect.DeepEqual(ops[1].All[0], plan.Predicate{Fact: "hostname_contains", Eq: "rocky"}) {
		t.Fatalf("when predicates = %#v", ops[1].All)
	}
	if !ops[2].Elevate {
		t.Fatalf("marker elevate lost: %#v", ops[2])
	}
}
