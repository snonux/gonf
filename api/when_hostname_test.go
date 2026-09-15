package api

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
)

func TestRecordPlanWhenHostnameHelper(t *testing.T) {
	ResetTasks()
	resource.ResetRepository()
	t.Cleanup(func() {
		resource.SetPlanDraftRecorder(nil)
		plan.SetRecording(false)
		plan.ResetRecord()
	})

	dir := t.TempDir()

	// One task carrying both hosts' schedules, gated inside the body with
	// the serializable hostname recipe (the DRY fleet pattern).
	Task("demo_cron", "", func() {
		WhenHostname("blowfish", func() {
			File(filepath.Join(dir, "blowfish.txt"), options.WithContent("x"))
		})
		WhenHostname("fishfinger", func() {
			File(filepath.Join(dir, "fishfinger.txt"), options.WithContent("x"))
		})
	}, Privileged())

	ops, err := RecordPlan("when-hostname", "", "demo_cron")
	if err != nil {
		t.Fatalf("RecordPlan: %v", err)
	}

	wantKinds := []plan.Kind{
		plan.KindPlan,
		plan.KindWhenBegin,
		plan.KindFile,
		plan.KindWhenEnd,
		plan.KindWhenBegin,
		plan.KindFile,
		plan.KindWhenEnd,
	}
	if len(ops) != len(wantKinds) {
		t.Fatalf("ops kinds = %v", opsKinds(ops))
	}
	for i, k := range wantKinds {
		if ops[i].Op != k {
			t.Fatalf("ops[%d]=%s want %s", i, ops[i].Op, k)
		}
	}

	for i, want := range []string{"blowfish", "fishfinger"} {
		begin := ops[1+i*3]
		if begin.All == nil || len(begin.All) != 1 ||
			begin.All[0] != (plan.Predicate{Fact: "hostname_contains", Eq: want}) {
			t.Fatalf("when_begin[%d] predicates = %#v", i, begin.All)
		}
		if begin.ID != "when.hostname:"+want {
			t.Fatalf("when_begin[%d] ID = %q", i, begin.ID)
		}
		// Privileged task: split promotion must see the block as elevated.
		if !ops[2+i*3].Elevate {
			t.Fatalf("op inside when block not elevated: %#v", ops[2+i*3])
		}
	}
}

func TestWhenHostnameLocal(t *testing.T) {
	ResetTasks()
	resource.ResetRepository()
	t.Cleanup(func() {
		resource.SetPlanDraftRecorder(nil)
		plan.SetRecording(false)
		plan.ResetRecord()
	})

	host, err := os.Hostname()
	if err != nil {
		t.Fatalf("os.Hostname: %v", err)
	}
	ran := false
	WhenHostname(host, func() { ran = true })
	if !ran {
		t.Fatal("expected fn to run for the matching hostname")
	}
	ran = false
	WhenHostname(host+"-zzz", func() { ran = true })
	if ran {
		t.Fatal("expected fn NOT to run for a non-matching hostname")
	}
	WhenHostname("", func() { ran = true })
	if !ran {
		t.Fatal("empty substr must always match")
	}
}
