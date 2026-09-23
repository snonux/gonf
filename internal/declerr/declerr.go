// Package declerr collects declaration errors: recipe misuse that the gonf
// DSL detects while a recipe declares its tasks, inventory and resources
// (an empty Task name, a duplicate Host, an option on a resource that does
// not support it, WithSensitive without WithName, ...), plus the record-time
// failures of DSL calls that cannot return an error (MustSecret, ForHosts).
//
// The DSL constructors return resources and handles, not errors, so they
// cannot hand such a failure back to the recipe. Before this package they
// ended the process with logger.Fatal (os.Exit), which skipped deferred
// cleanup and made gonf unusable as an embedded library. Instead, a misuse is
// now reported here and the constructor returns an inert value (an
// unregistered resource, an empty Multi, a zero handle) so later declarations
// keep working. The error surfaces where the recipe is consumed:
//
//   - while a plan is recorded (RecordPlanTo installs a sink with Capture), the
//     report fails that recording session like any other stashed task-body
//     error; RecordPlanTo also resets the registered resource repository on
//     that failure, so nothing the failed body registered survives it;
//   - otherwise (top-level registration in main, direct api.Apply use) the
//     FIRST report is kept and api.RecordPlanTo, api.Run, api.Apply and the
//     CLI refuse to proceed with it — sticky for the rest of the process, on
//     purpose (task ad2/id2's reasoning: a later, unrelated registration
//     must not silently mask it), even once the recipe that caused it is
//     fixed. A library embedder that wants to keep using the process after
//     fixing that recipe clears it with resource.ResetDeclarationError
//     (task oe2), the production-safe counterpart of the resource.
//     ResetForTest test seam.
//
// A sink swallows a report for First's purposes: the recording session
// handles it (RecordPlanTo's caller gets it as the record's returned error),
// not the process-wide sticky error. Tracking whether SOME past record ever
// failed — misuse or otherwise — so a later api.Apply call can refuse
// instead of silently applying (or no-op'ing on) leftover state is
// RecordPlanTo's own job (api/plan.go), not this package's: a record can
// also fail for reasons that never reach Report at all (a task recursion
// cycle, a packaging error), and this package only ever hears about
// declared misuse.
//
// Reports are first-error-wins: a later misuse is usually a consequence of the
// first one, and the operator fixes the first one first. Each report carries
// the location of the recipe code that caused it (Location), found by walking
// the stack past gonf's own frames.
package declerr

import (
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
)

// modulePrefix is the import path prefix of gonf's own packages. Frames in it
// are skipped when looking for the recipe line that caused a report.
const modulePrefix = "github.com/snonux/gonf/"

// maxFrames bounds the stack walk of callerLocation; DSL call chains are far
// shallower than this.
const maxFrames = 64

// Error is a reported declaration error. Error() is exactly the message of
// the wrapped error, so the text an operator sees is the same as the one
// logger.Fatal used to print; the recipe location is kept separately.
type Error struct {
	// Err is the misuse itself.
	Err error
	// Location is "file:line" of the recipe code that caused the report, or
	// "" when no frame outside gonf (or a gonf test) was found.
	Location string
}

var (
	mu sync.Mutex
	// first is the sticky first report made while no sink was installed.
	first error
	// sink, when set, receives every report instead of first (Capture).
	sink func(error)
)

// Error returns the wrapped error's message.
func (e *Error) Error() string { return e.Err.Error() }

// Unwrap returns the wrapped error, so errors.Is/As see through the report.
func (e *Error) Unwrap() error { return e.Err }

// Report records err as a declaration error. When a sink is installed
// (Capture) err goes to it; otherwise the first report is kept for First and
// later ones are dropped. A nil err is ignored. Report never logs: the error
// is printed once, by whoever surfaces it.
func Report(err error) {
	if err == nil {
		return
	}
	var de *Error
	if !errors.As(err, &de) {
		err = &Error{Err: err, Location: callerLocation()}
	}
	mu.Lock()
	s := sink
	if s == nil && first == nil {
		first = err
	}
	mu.Unlock()
	if s != nil {
		s(err)
	}
}

// Reportf is Report(fmt.Errorf(format, args...)).
func Reportf(format string, args ...any) {
	Report(fmt.Errorf(format, args...))
}

// First returns the first declaration error reported while no sink was
// installed, or nil. It stays set for the life of the process (a broken
// registration does not heal); tests clear it with Reset.
func First() error {
	mu.Lock()
	defer mu.Unlock()
	return first
}

// Capture routes every Report to fn until restore is called, which reinstates
// the previous sink. api.RecordPlanTo uses it to fail the current recording
// session with a misuse inside a task body. fn runs outside the package lock
// and must not call Capture.
func Capture(fn func(error)) (restore func()) {
	mu.Lock()
	prev := sink
	sink = fn
	mu.Unlock()
	return func() {
		mu.Lock()
		defer mu.Unlock()
		sink = prev
	}
}

// Reset clears the sticky first error and any installed sink. Since this
// package is internal/, only gonf's own public packages can call it:
// resource.ResetForTest (a test seam that also wipes other resource state)
// and resource.ResetDeclarationError (its production-safe, single-purpose
// equivalent for a library embedder, task oe2) both do, and must not run
// concurrently with a recording.
func Reset() {
	mu.Lock()
	defer mu.Unlock()
	first = nil
	sink = nil
}

// Location returns the recipe location recorded with err, or "" when err is
// not (and does not wrap) a declaration Error or the location is unknown.
func Location(err error) string {
	var de *Error
	if errors.As(err, &de) {
		return de.Location
	}
	return ""
}

// callerLocation returns "file:line" of the first stack frame outside gonf's
// own packages — the recipe line that called the DSL — or of a gonf _test.go
// frame, so in-repo tests get a location too. It returns "" when neither
// exists.
func callerLocation() string {
	pcs := make([]uintptr, maxFrames)
	n := runtime.Callers(3, pcs) // skip runtime.Callers, callerLocation, Report
	frames := runtime.CallersFrames(pcs[:n])
	for {
		f, more := frames.Next()
		if isRecipeFrame(f) {
			return fmt.Sprintf("%s:%d", f.File, f.Line)
		}
		if !more {
			return ""
		}
	}
}

// isRecipeFrame reports whether f belongs to recipe code: not the Go runtime,
// reflection (RegisterMethods calls task companions through it) or testing
// machinery, and either outside gonf's module or in a gonf test
// file.
func isRecipeFrame(f runtime.Frame) bool {
	switch {
	case f.Function == "" || strings.HasPrefix(f.Function, "runtime.") ||
		strings.HasPrefix(f.Function, "testing.") || strings.HasPrefix(f.Function, "reflect."):
		return false
	case strings.HasSuffix(f.File, "_test.go"):
		return true
	default:
		return !strings.HasPrefix(f.Function, modulePrefix)
	}
}
