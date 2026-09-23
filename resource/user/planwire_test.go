package user

import (
	"fmt"
	"strings"
	"testing"

	"github.com/snonux/gonf/resource"
)

// TestToOpRejectsOnlyRequestsEveryBackendRefuses pins the record-time check
// (task 472): the target platform is unknown while recording, so a request is
// rejected only when no backend could apply it, and anything some platform
// accepts still records.
func TestToOpRejectsOnlyRequestsEveryBackendRefuses(t *testing.T) {
	groups := make([]string, 17)
	for i := range groups {
		groups[i] = fmt.Sprintf("g%02d", i)
	}
	tests := []struct {
		name  string
		draft resource.PlanDraft
		err   string
	}{
		{"login class records (BSD accepts it)", resource.PlanDraft{Name: "svc", Payload: Payload{LoginClass: "daemon"}}, ""},
		{"many groups record (Linux and FreeBSD accept them)", resource.PlanDraft{Name: "svc", Payload: Payload{SupplementaryGroups: groups}}, ""},
		{"system account records (Linux accepts it)", resource.PlanDraft{Name: "svc", Payload: Payload{System: true}}, ""},
		{"relative created home records (Linux and BSD accept it)", resource.PlanDraft{Name: "svc", Payload: Payload{Home: "rel", CreateHome: true}}, ""},
		{"malformed name", resource.PlanDraft{Name: "-svc", Payload: Payload{}}, "starts with -"},
		{"malformed group", resource.PlanDraft{Name: "svc", Payload: Payload{PrimaryGroup: "a,b"}}, "comma"},
		{"refused by every platform", resource.PlanDraft{Name: "svc", Payload: Payload{LoginClass: "daemon", SupplementaryGroups: groups, Home: "rel", CreateHome: true}}, "no supported platform accepts this request"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.draft.Kind, tt.draft.ID = "user", "User["+tt.draft.Name+"]"
			_, err := (planHandler{}).ToOp(tt.draft)
			if tt.err == "" {
				if err != nil {
					t.Fatalf("ToOp() = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.err) {
				t.Fatalf("ToOp() = %v, want %q", err, tt.err)
			}
		})
	}
}

// TestToOpKeepsManagedHomeErrorText pins that an opted-in managed home is
// still reported with ValidateManagedHome's text at record time, even where
// the generic request validation would also object (a NUL byte).
func TestToOpKeepsManagedHomeErrorText(t *testing.T) {
	_, err := (planHandler{}).ToOp(resource.PlanDraft{Kind: "user", ID: "User[svc]", Name: "svc", Payload: Payload{Home: "/var/run/a\x00b", ManageHome: true}})
	want := `user "svc": managed home "/var/run/a\x00b" contains ':', a line break, or NUL`
	if err == nil || err.Error() != want {
		t.Fatalf("ToOp() = %v, want %q", err, want)
	}
}
