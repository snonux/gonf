package api

import (
	"reflect"
	"testing"

	"github.com/snonux/gonf/api/options"
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
