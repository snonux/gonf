package options

import "testing"

type manageHomeTarget struct{ calls int }

func (m *manageHomeTarget) SetManageHome() { m.calls++ }

func TestWithManageHomeIsALocalUserOptionReachingItsSetter(t *testing.T) {
	acceptLocalUser(WithManageHome)
	target := &manageHomeTarget{}
	WithManageHome.Apply(target)
	if target.calls != 1 {
		t.Fatalf("SetManageHome calls = %d, want 1", target.calls)
	}
	if got := len(ToLocalUserOptions(WithManageHome)); got != 1 {
		t.Fatalf("ToLocalUserOptions(WithManageHome) = %d options, want 1", got)
	}
}
