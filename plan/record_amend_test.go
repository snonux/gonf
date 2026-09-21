package plan_test

import (
	"strings"
	"testing"

	"github.com/snonux/gonf/plan"
)

// recordAmendFixture starts a recording session holding ops; the cleanup
// disables recording and clears the ops again.
func recordAmendFixture(t *testing.T, ops ...plan.Op) {
	t.Helper()
	plan.ResetRecord()
	plan.SetRecording(true)
	t.Cleanup(func() {
		plan.SetRecording(false)
		plan.ResetRecord()
	})
	for _, op := range ops {
		plan.Record(op)
	}
}

// TestAmendRecordedReplacesLatestOpInPlace pins that the most recent op
// carrying the id is replaced at its recorded position, with the amend
// callback seeing the recorded op (so it can keep fields such as Elevate).
func TestAmendRecordedReplacesLatestOpInPlace(t *testing.T) {
	recordAmendFixture(t,
		plan.Op{Op: plan.KindDaemonReload, ID: "DaemonReload[system]"},
		plan.Op{Op: plan.KindFile, ID: "File[/a]", Elevate: true},
		plan.Op{Op: plan.KindDaemonReload, ID: "DaemonReload[system]", Elevate: true},
		plan.Op{Op: plan.KindFile, ID: "File[/b]", Elevate: true},
	)
	err := plan.AmendRecorded("DaemonReload[system]", func(op plan.Op) (plan.Op, error) {
		if !op.Elevate {
			t.Fatalf("amend saw %#v, want the most recent recorded op", op)
		}
		op.Deps = []string{"File[/a]", "File[/b]"}
		return op, nil
	})
	if err != nil {
		t.Fatalf("AmendRecorded: %v", err)
	}
	got := plan.Recorded()
	if len(got) != 4 || got[2].ID != "DaemonReload[system]" || len(got[2].Deps) != 2 {
		t.Fatalf("recorded = %#v, want the reload amended at index 2", got)
	}
	if got[0].Deps != nil {
		t.Fatalf("an earlier op with the same id was amended: %#v", got[0])
	}
}

// TestAmendRecordedRefusesAcrossBoundaries is the negative case: a when-block
// boundary or a privilege change recorded after the target makes the
// amendment unsound, so it is refused and the recorded ops stay unchanged.
func TestAmendRecordedRefusesAcrossBoundaries(t *testing.T) {
	cases := map[string]plan.Op{
		"when_end":   {Op: plan.KindWhenEnd},
		"when_begin": {Op: plan.KindWhenBegin, ID: "when.x"},
		"privilege":  {Op: plan.KindFile, ID: "File[/root]", Elevate: true},
	}
	for name, boundary := range cases {
		t.Run(name, func(t *testing.T) {
			recordAmendFixture(t, plan.Op{Op: plan.KindDaemonReload, ID: "DaemonReload[system]"}, boundary)
			err := plan.AmendRecorded("DaemonReload[system]", func(op plan.Op) (plan.Op, error) {
				t.Fatal("amend callback ran across a boundary")
				return op, nil
			})
			if err == nil || !strings.Contains(err.Error(), "DaemonReload[system]") {
				t.Fatalf("AmendRecorded err = %v, want a refusal naming the op", err)
			}
			if got := plan.Recorded(); got[0].Deps != nil {
				t.Fatalf("refused amendment changed the op: %#v", got[0])
			}
		})
	}
}

// TestAmendRecordedRefusesPrivilegeChange: an amended op lowered under
// another privilege than the recorded one is refused and nothing changes.
func TestAmendRecordedRefusesPrivilegeChange(t *testing.T) {
	recordAmendFixture(t, plan.Op{Op: plan.KindDaemonReload, ID: "DaemonReload[system]", Elevate: true})
	err := plan.AmendRecorded("DaemonReload[system]", func(op plan.Op) (plan.Op, error) {
		op.Elevate = false
		op.Deps = []string{"File[/a]"}
		return op, nil
	})
	if err == nil || !strings.Contains(err.Error(), "different privilege chunks") {
		t.Fatalf("AmendRecorded err = %v, want a privilege refusal", err)
	}
	if got := plan.Recorded(); !got[0].Elevate || got[0].Deps != nil {
		t.Fatalf("refused amendment changed the op: %#v", got[0])
	}
}

// TestAmendRecordedNoOpWithoutRecordedOp pins the no-op cases: recording off,
// or no op carrying the id.
func TestAmendRecordedNoOpWithoutRecordedOp(t *testing.T) {
	plan.ResetForTest()
	called := false
	amend := func(op plan.Op) (plan.Op, error) { called = true; return op, nil }
	if err := plan.AmendRecorded("X[y]", amend); err != nil || called {
		t.Fatalf("recording off: err=%v called=%v, want nil and no call", err, called)
	}
	recordAmendFixture(t, plan.Op{Op: plan.KindFile, ID: "File[/a]"})
	if err := plan.AmendRecorded("X[y]", amend); err != nil || called {
		t.Fatalf("unknown id: err=%v called=%v, want nil and no call", err, called)
	}
}
