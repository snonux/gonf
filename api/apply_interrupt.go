package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	gexec "github.com/snonux/gonf/internal/exec"
)

// validatorWaitNotice is the line printed, by the process running it (the
// elevated child for a privileged op), when an interrupt arrives while a
// File or ConfigSet validator runs; %v is the command timeout bounding it.
// The verdict is discarded and the candidate not published either way (see
// internal/validator runIn).
const validatorWaitNotice = "gonf: interrupt received; waiting for the running validator to finish " +
	"(bounded by the command timeout, %v); its candidate will not be published\n"

// noteValidatorWait arranges that, once ctx is canceled (SIGINT/SIGTERM),
// the operator is told on w when a validator (running reports one) is still
// executing. Validators are deliberately not bound to the apply's signal
// context, only to the command timeout, so without this an interrupted apply
// would seem to hang silently for up to that timeout. A deadline is not an
// interrupt and prints nothing. The returned stop disarms the notice and, if
// it had already started (afterFuncJoined), blocks until it finishes; call
// it when the apply returns. Safe to call more than once (afterFuncJoined's
// stop is idempotent, task 8d2): a caller of noteValidatorWait may combine a
// deferred call with an earlier one on some other return path. w is
// commonly os.Stderr (ApplyPlanContext), a package-level variable other code
// (e.g. a test's CaptureStderr) may swap concurrently, so joining here —
// instead of leaving the notice goroutine to finish on its own time —
// matters, not just tidiness (task 1d2).
func noteValidatorWait(ctx context.Context, w io.Writer, running func() bool) (stop func()) {
	return afterFuncJoined(ctx, func() {
		if errors.Is(ctx.Err(), context.Canceled) && running() {
			_, _ = fmt.Fprintf(w, validatorWaitNotice, gexec.DefaultTimeout())
		}
	})
}

// afterFuncJoined wraps context.AfterFunc(ctx, f): f still runs once ctx is
// done, but unlike context.AfterFunc's own stop, the returned stop blocks
// until f has actually finished whenever it could not be prevented from
// starting. Plain context.AfterFunc's stop only reports whether it managed
// to prevent f from starting; if f already started, stop returns immediately
// and f keeps running on its own goroutine, unobserved by the caller. That
// let a f touching a shared package variable (os.Stderr, via
// runElevatedCmd's and noteValidatorWait's own notices) still be mid-write
// after the function that deferred stop had already returned — the goroutine
// leak that raced TestRunElevatedCmdCancelAfterChildExitsDoesNotPanic's
// stderr write against a later test's os.Stderr swap under
// -race -shuffle=on (task 1d2). Joining here bounds f's lifetime to the
// call that registered it, closing that hazard structurally rather than
// coordinating around it at each call site.
//
// The returned stop is idempotent-safe: call it any number of times (both
// existing call sites here only ever defer it once, but noteValidatorWait
// hands its stop back to ITS OWN caller as a return value, so a future
// caller is free to also call it early alongside an existing deferred call
// without auditing this function first). Without the sync.Once below, a
// second call would hang forever: context.AfterFunc's own rawStop returns
// false both when f already started AND when stop was already called
// before — this wrapper's body only handled the first meaning, so a second
// call would see rawStop() return false again (now meaning "already
// stopped", not "already started") and block on <-done past the point
// where f (and its close(done)) will ever run again — confirmed directly
// with a real stop(); stop() probe (task 8d2) rather than assumed from
// context.AfterFunc's doc prose alone. sync.Once makes every call after the
// first a no-op that returns immediately, exactly like calling an
// already-fired context.CancelFunc a second time.
func afterFuncJoined(ctx context.Context, f func()) (stop func()) {
	done := make(chan struct{})
	rawStop := context.AfterFunc(ctx, func() {
		defer close(done)
		f()
	})
	var once sync.Once
	return func() {
		once.Do(func() {
			if !rawStop() {
				<-done
			}
		})
	}
}
