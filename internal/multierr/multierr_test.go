package multierr

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

// typedErr checks errors.As reaches a typed member.
type typedErr struct{ code int }

func (e *typedErr) Error() string { return fmt.Sprintf("typed %d", e.code) }

func TestJoinSortedNilWhenNoErrors(t *testing.T) {
	if err := JoinSorted("p", nil); err != nil {
		t.Fatalf("JoinSorted(nil) = %v, want nil", err)
	}
	if err := JoinSorted("p", []error{nil, nil}); err != nil {
		t.Fatalf("JoinSorted(all nil) = %v, want nil", err)
	}
}

// The message is the historical single line: prefix, ": ", members sorted by
// message and joined with "; ". Input order is left untouched.
func TestJoinSortedMessageAndOrder(t *testing.T) {
	in := []error{errors.New("b: two"), nil, errors.New("a: one")}
	err := JoinSorted(`cluster "x"`, in)
	if got, want := err.Error(), `cluster "x": a: one; b: two`; got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
	if in[0].Error() != "b: two" || in[1] != nil {
		t.Fatalf("input slice was modified: %v", in)
	}
}

// errors.Is / errors.As reach every member's chain, including nested
// aggregates (a fleet error of cluster errors of host errors).
func TestJoinSortedUnwrapsMembers(t *testing.T) {
	typed := &typedErr{code: 7}
	inner := JoinSorted(`cluster "c"`, []error{
		fmt.Errorf("h1: %w", typed),
		fmt.Errorf("h2: %w", context.DeadlineExceeded),
	})
	outer := JoinSorted(`fleet "f"`, []error{inner, fmt.Errorf("cluster %q: aborted: %w", "d", context.Canceled)})

	var got *typedErr
	if !errors.As(outer, &got) || got != typed {
		t.Fatalf("errors.As(*typedErr) failed: %v", outer)
	}
	for _, target := range []error{context.DeadlineExceeded, context.Canceled} {
		if !errors.Is(outer, target) {
			t.Fatalf("errors.Is(%v) failed: %v", target, outer)
		}
	}
	var agg *Error
	if !errors.As(outer, &agg) || agg.Prefix != `fleet "f"` || len(agg.Errs) != 2 {
		t.Fatalf("errors.As(*Error) = %+v", agg)
	}
}
