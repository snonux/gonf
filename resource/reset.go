package resource

// ResetForTest is the single canonical test seam for resource package state: it
// swaps in an empty repository, clears the apply report, turns dry-run off, and
// clears the sticky declaration error (internal/declerr, via
// ResetDeclarationError) that DSL misuse reported, which every resource
// constructor feeds. The individual functions it composes (ResetRepository,
// ResetReport, SetDryRun, ResetDeclarationError) stay available for callers
// that reset exactly one piece, so existing tests keep working; a
// production caller that only wants to clear the sticky declaration error
// (e.g. a library embedder recovering from a fixed recipe collision) should
// call ResetDeclarationError directly instead of this test seam — see its
// doc comment.
//
// Like everything in this package it is deliberately single-goroutine: call
// it only while no registration or apply is in flight (see
// repository.go for the DSL invariant).
func ResetForTest() {
	ResetRepository()
	ResetReport()
	SetDryRun(false)
	ResetDeclarationError()
}
