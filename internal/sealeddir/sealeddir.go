// Package sealeddir carries, for exactly one apply, the private directory
// that holds the decrypted copies of a push's sealed sticky-dir refs (w82
// phase 4, task 0g2; docs/plan-encryption.md, "Phase 4 design: sealed
// multi-chunk sticky-dir blobs").
//
// A multi-chunk push stages its blobs in one sticky directory owned by the
// SSH login user. The refs a sensitive op of an elevated chunk reads are
// uploaded there sealed, never as plaintext; the elevated chunk decrypts
// them into its own fresh 0700 plan.NewSealedApplyRunDir() before it
// applies (internal/cli). Every other ref of that chunk still lives in the
// sticky dir, so one apply needs two plan directories: the private one for
// the sealed refs and the sticky one for the rest. plan.ApplyWithContext
// takes a single planDir, so the private one travels here instead, as a
// context value that plan's applyActiveWithFacts consults (Resolve) when it
// builds each op's plan.ApplyContext.PlanDir. resource/file and
// resource/dir therefore keep reading an ordinary plaintext blob from
// whatever PlanDir they are given and need no change at all.
//
// Like internal/runners, only code inside this module can import this
// package, and only this package can construct the unexported context key,
// so an external recipe module can neither set nor forge the override.
package sealeddir

import "context"

// ctxKey is the unexported type of the context key With/Resolve use.
type ctxKey struct{}

// override is the private directory and the set of blob refs it serves.
type override struct {
	dir  string
	refs map[string]bool
}

// With returns a context under which Resolve maps every ref in refs to dir
// (the private run dir the sealed refs were decrypted into). An empty dir
// or refs returns ctx unchanged, so a chunk without sealed refs resolves
// exactly as before.
func With(ctx context.Context, dir string, refs []string) context.Context {
	if dir == "" || len(refs) == 0 {
		return ctx
	}
	set := make(map[string]bool, len(refs))
	for _, ref := range refs {
		set[ref] = true
	}
	return context.WithValue(ctx, ctxKey{}, override{dir: dir, refs: set})
}

// Resolve returns the plan directory an op reading blobRef must use: the
// private directory With attached to ctx when blobRef is one of its sealed
// refs, otherwise planDir unchanged. A sealed ref therefore never falls
// back to planDir (the login user's sticky dir), whatever that holds.
func Resolve(ctx context.Context, blobRef, planDir string) string {
	if blobRef == "" {
		return planDir
	}
	if o, ok := ctx.Value(ctxKey{}).(override); ok && o.refs[blobRef] {
		return o.dir
	}
	return planDir
}
