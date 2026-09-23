package resource

import "github.com/snonux/gonf/internal/declerr"

// ResetForTest is the single canonical test seam for resource package state: it
// swaps in an empty repository, clears the apply report, turns dry-run off, and
// clears BOTH the sticky declaration error and any installed capture sink
// (internal/declerr.Reset) that DSL misuse reported, which every resource
// constructor feeds. It calls declerr.Reset directly rather than going
// through ResetDeclarationError: since task tf2, ResetDeclarationError only
// clears the sticky first error (internal/declerr.ResetFirst), deliberately
// leaving an active recording's capture sink alone — exactly the opposite of
// what a between-tests wipe needs, since no test should leave a stale sink
// installed for the next one to capture into by accident. ResetRepository,
// ResetReport and SetDryRun stay available for callers that reset exactly
// one piece, so existing tests keep working; a production caller that only
// wants to clear the sticky declaration error (e.g. a library embedder
// recovering from a fixed recipe collision) should call
// ResetDeclarationError directly instead of this test seam — see its own
// doc comment, including the secret-resolution-failure class it warns is
// NOT safe to clear-and-continue from.
//
// Like everything in this package it is deliberately single-goroutine: call
// it only while no registration or apply is in flight (see
// repository.go for the DSL invariant).
func ResetForTest() {
	ResetRepository()
	ResetReport()
	SetDryRun(false)
	declerr.Reset()
}
