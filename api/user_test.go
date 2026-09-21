package api

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
)

func TestUserPublicDSLRecordsPlanBackedDraft(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)

	got := User("svc",
		options.WithPrimaryGroup("svc"),
		options.WithUserGroup("wheel"),
		options.WithSupplementaryGroups("audio"),
		options.WithHome("/var/lib/svc"),
		options.WithCreateHome,
		options.WithShell("/sbin/nologin"),
		options.WithLoginClass("daemon"),
	)
	if got.ID() != "User[svc]" {
		t.Fatalf("User() id = %q", got.ID())
	}
	drafts := resource.RegisteredPlanDrafts()
	if len(drafts) != 1 {
		t.Fatalf("draft count = %d, want 1", len(drafts))
	}
	if drafts[0].Kind != "user" || drafts[0].PrimaryGroup != "svc" ||
		!reflect.DeepEqual(drafts[0].SupplementaryGroups, []string{"wheel", "audio"}) ||
		drafts[0].LoginClass != "daemon" {
		t.Fatalf("user draft = %#v", drafts[0])
	}
	op, err := draftToOp(drafts[0])
	if err != nil {
		t.Fatalf("draftToOp() = %v", err)
	}
	if op.Op != "user" || op.Name != "svc" || op.PrimaryGroup != "svc" ||
		!reflect.DeepEqual(op.SupplementaryGroups, []string{"wheel", "audio"}) ||
		op.LoginClass != "daemon" {
		t.Fatalf("user op = %#v", op)
	}
}

// TestUserManageHomeRecordsVersionedPlan records an opted-in and a plain
// user through the public DSL and pins the encoded bytes: the header must
// carry at least the schema version that introduced manage_home (so a merge
// that loses the bump fails here), the opted-in op must carry manage_home,
// and the plain op must encode exactly as it did before the option existed.
func TestUserManageHomeRecordsVersionedPlan(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)
	Task("svc_home", "", func() {
		User("_dserver", options.WithPrimaryGroup("_dserver"), options.WithHome("/var/svc/dserver"), options.WithManageHome)
		User("_plain", options.WithPrimaryGroup("_plain"), options.WithHome("/var/svc/plain"))
	})
	ops, err := RecordPlanTo("home", plan.NewMemoryStore(), "svc_home")
	if err != nil {
		t.Fatalf("RecordPlanTo() = %v", err)
	}
	if plan.VersionUserManageHome != 19 || plan.CurrentVersion < plan.VersionUserManageHome {
		t.Fatalf("VersionUserManageHome = %d, CurrentVersion = %d; manage_home was introduced by v19 and must not be emitted under an older header",
			plan.VersionUserManageHome, plan.CurrentVersion)
	}
	encoded, err := plan.EncodePlan(ops)
	if err != nil {
		t.Fatalf("EncodePlan() = %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(encoded)), "\n")
	want := []string{
		fmt.Sprintf(`{"op":"plan","version":%d,"id":"home"}`, plan.CurrentVersion),
		`{"op":"user","id":"User[_dserver]","primary_group":"_dserver","home":"/var/svc/dserver","manage_home":true,"name":"_dserver"}`,
		`{"op":"user","id":"User[_plain]","primary_group":"_plain","home":"/var/svc/plain","name":"_plain"}`,
	}
	if !reflect.DeepEqual(lines, want) {
		t.Fatalf("encoded plan:\n%s\nwant:\n%s", strings.Join(lines, "\n"), strings.Join(want, "\n"))
	}
}

// TestUserManageHomeWithoutHomeFailsRecording is the negative record-time
// case: an opt-in without a home must fail plan recording on the controller
// rather than on the destination.
func TestUserManageHomeWithoutHomeFailsRecording(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)
	Task("svc_nohome", "", func() {
		User("_dserver", options.WithManageHome)
	})
	_, err := RecordPlanTo("nohome", plan.NewMemoryStore(), "svc_nohome")
	if err == nil || !strings.Contains(err.Error(), "requires a home directory") {
		t.Fatalf("RecordPlanTo() = %v, want managed-home error", err)
	}
}
