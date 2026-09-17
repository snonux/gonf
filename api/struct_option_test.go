package api

import (
	"path/filepath"
	"reflect"
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

// A method's own OptsX companion REPLACES the embedded marker: the empty
// opt-out must win over RequiresRoot.
type markerWithOptOut struct{ dir string }

func (markerWithOptOut) OptsSmoke() TaskOptions { return TaskOptions{} }

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
		t.Fatalf("OptsX empty must replace the embedded marker: %#v", ops[1])
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
