package api

import (
	"testing"

	"github.com/snonux/gonf/plan"
)

// TestRecordOnlyOpIDsKeepTheirWireSpelling pins task 272's IDs for the two
// record-only recipes. LinkIfExists and EnsureDir register no resource in
// record mode (they return an empty Multi, so nothing can depend on them);
// their op ID exists only on the plan wire, where it orders and names the
// op. A changed spelling would silently change every recorded plan, so the
// exact bytes are pinned here.
func TestRecordOnlyOpIDsKeepTheirWireSpelling(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)
	Task("record_only", "", func() {
		LinkIfExists("/srv/link", "/srv/target")
		EnsureDir("/srv/dir")
	})
	ops, err := RecordPlanTo("ids", plan.NewMemoryStore(), "record_only")
	if err != nil {
		t.Fatalf("RecordPlanTo() = %v", err)
	}
	want := map[plan.Kind]string{
		plan.KindLinkIfExists: "LinkIfExists[/srv/link]",
		plan.KindEnsureDir:    "EnsureDir[/srv/dir]",
	}
	for _, op := range ops {
		if id, ok := want[op.Op]; ok {
			if op.ID != id {
				t.Errorf("%s op ID = %q, want %q", op.Op, op.ID, id)
			}
			delete(want, op.Op)
		}
	}
	for kind := range want {
		t.Errorf("no %s op recorded", kind)
	}
}
