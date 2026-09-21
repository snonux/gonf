// Package multierr aggregates the per-member failures of a fan-out (one error
// per host of a cluster push, one per cluster group of a fleet push) into one
// error that keeps every cause inspectable.
//
// The standard library's errors.Join also unwraps to all its members, but it
// separates them with newlines. gonf's fan-out errors have always been a
// single "<prefix>: a; b" line, which the CLI prints and users grep, so this
// package keeps that exact wording while still implementing Unwrap() []error:
// errors.Is and errors.As reach through the aggregate to any member's chain
// (a plan.Refusal, a *plan.DanglingDepError, context.DeadlineExceeded, ...).
package multierr

import (
	"sort"
	"strings"
)

// Separator joins member messages in an aggregate's Error text.
const Separator = "; "

// Error is a prefixed aggregate of member errors. Its message is
// Prefix + ": " + the members' messages joined with Separator, in member
// order; Unwrap exposes the members to errors.Is / errors.As.
type Error struct {
	Prefix string
	Errs   []error
}

// Error renders the single-line aggregate message.
func (e *Error) Error() string {
	msgs := make([]string, len(e.Errs))
	for i, err := range e.Errs {
		msgs[i] = err.Error()
	}
	return e.Prefix + ": " + strings.Join(msgs, Separator)
}

// Unwrap returns the member errors (the Go 1.20 multi-error form), so
// errors.Is / errors.As walk every member's chain.
func (e *Error) Unwrap() []error { return e.Errs }

// JoinSorted returns nil when errs holds no non-nil error; otherwise an *Error
// with the non-nil members ordered by message. Sorting keeps the aggregate
// deterministic although fan-out members finish in arbitrary order. errs is
// not modified.
func JoinSorted(prefix string, errs []error) error {
	members := make([]error, 0, len(errs))
	for _, err := range errs {
		if err != nil {
			members = append(members, err)
		}
	}
	if len(members) == 0 {
		return nil
	}
	// Stable so members with identical text keep their relative order.
	sort.SliceStable(members, func(i, j int) bool {
		return members[i].Error() < members[j].Error()
	})
	return &Error{Prefix: prefix, Errs: members}
}
