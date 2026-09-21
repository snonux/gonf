package api

import (
	"errors"
	"testing"

	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
)

// TestRecordPlanToClearsAmender pins that RecordPlanTo installs the draft
// amend sink only for its own session: it is set while task bodies record
// and cleared on every return path (success, a failing task body, an unknown
// task), so a later api.Apply or legacy registration never amends a plan
// that is no longer being recorded.
func TestRecordPlanToClearsAmender(t *testing.T) {
	t.Cleanup(func() {
		resource.SetPlanDraftRecorder(nil)
		resource.SetPlanDraftAmender(nil)
		plan.SetRecording(false)
		plan.ResetRecord()
	})
	cases := map[string]struct {
		body    func()
		task    string
		wantErr bool
	}{
		"success":      {body: func() {}, task: "amender_probe"},
		"body-error":   {body: func() { stashBodyError(errors.New("boom")) }, task: "amender_probe", wantErr: true},
		"unknown-task": {body: func() {}, task: "amender_missing", wantErr: true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ResetTasks()
			resource.ResetRepository()
			sawSink := false
			Task("amender_probe", "probe", func() {
				sawSink = resource.PlanDraftAmending()
				tc.body()
			})
			_, err := RecordPlanTo("p", plan.NewMemoryStore(), tc.task)
			if (err != nil) != tc.wantErr {
				t.Fatalf("RecordPlanTo err = %v, wantErr %v", err, tc.wantErr)
			}
			if tc.task == "amender_probe" && !sawSink {
				t.Fatal("amend sink not installed while the task body recorded")
			}
			if resource.PlanDraftAmending() {
				t.Fatal("amend sink still installed after RecordPlanTo returned")
			}
		})
	}
}
