package api

import (
	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
)

// ResetForTest is the single canonical test seam for api package ambient
// state. It clears the task registry (ResetTasks), the profile override, the
// plan recording session (api/plan.go), plan record mode (plan.ResetForTest),
// and the resource package state (repository, report, dry-run; via
// resource.ResetForTest). Individual reset functions remain available so
// existing tests keep working.
//
// Like the DSL itself this is deliberately single-goroutine: call it only
// while no registration, recording, or apply is in flight. It does not touch
// the CLI-only knobs processPrivilege and elevatedApplyRunner (both api-side,
// the latter also the local apply engine), nor the SSH transport hook
// (internal/remote.SSHRunner); tests that override those restore them with
// t.Cleanup.
func ResetForTest() {
	ResetTasks()           // task registry (see ResetTasks)
	SetProfileOverride("") // CLI -profile must not leak between tests
	recSession.reset()     // plan recording session (api/plan.go)
	plan.ResetForTest()
	resource.ResetForTest()
}
