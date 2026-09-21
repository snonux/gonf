package options

import "testing"

type manageHomeTarget struct{ calls int }

func (m *manageHomeTarget) SetManageHome() { m.calls++ }

// TestWithManageHomeReExportReachesItsSetter checks the public re-export is a
// local-user option wired to the same setter as the resource-level option.
func TestWithManageHomeReExportReachesItsSetter(t *testing.T) {
	var _ LocalUserOption = WithManageHome
	var _ HomeManageable = (*manageHomeTarget)(nil)
	target := &manageHomeTarget{}
	WithManageHome.Apply(target)
	if target.calls != 1 {
		t.Fatalf("SetManageHome calls = %d, want 1", target.calls)
	}
}
