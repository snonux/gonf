package resource

// ResetForTest is the single canonical test seam for resource package state:
// it swaps in an empty repository, clears the apply report, and turns
// dry-run off. The individual functions it composes (ResetRepository,
// ResetReport, SetDryRun) stay available for callers that reset exactly one
// piece, so existing tests keep working.
//
// Like everything in this package it is deliberately single-goroutine: call
// it only while no registration or apply is in flight (see
// repository.go for the DSL invariant).
func ResetForTest() {
	ResetRepository()
	ResetReport()
	SetDryRun(false)
}
