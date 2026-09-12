package resource_test

import (
	"testing"

	"github.com/snonux/gonf/resource"
)

func TestPlanDraftRecorder(t *testing.T) {
	var got []resource.PlanDraft
	resource.SetPlanDraftRecorder(func(d resource.PlanDraft) {
		got = append(got, d)
	})
	t.Cleanup(func() { resource.SetPlanDraftRecorder(nil) })

	if !resource.PlanDraftRecording() {
		t.Fatal("expected recording enabled")
	}
	resource.RecordPlanDraft(resource.PlanDraft{Kind: "package", Name: "fish"})
	if len(got) != 1 || got[0].Name != "fish" {
		t.Fatalf("got %#v", got)
	}

	resource.SetPlanDraftRecorder(nil)
	if resource.PlanDraftRecording() {
		t.Fatal("expected recording disabled")
	}
	resource.RecordPlanDraft(resource.PlanDraft{Kind: "package", Name: "helix"})
	if len(got) != 1 {
		t.Fatalf("disabled recorder should ignore drafts, got %#v", got)
	}
}
