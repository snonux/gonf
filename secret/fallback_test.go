package secret

import (
	"context"
	"errors"
	"testing"
)

// TestFallbackPrefersPrimary confirms a reference primary holds is answered
// from primary, and secondary is never consulted — the common, already-
// migrated case.
func TestFallbackPrefersPrimary(t *testing.T) {
	primary := &fakeStore{values: map[Ref]string{"a": "new-value"}}
	secondary := &fakeStore{values: map[Ref]string{"a": "old-value"}}
	f := NewFallback(primary, secondary)

	data, err := Resolve(context.Background(), f, "a")
	if err != nil || string(data) != "new-value" {
		t.Fatalf("Resolve = (%q, %v), want (\"new-value\", nil)", data, err)
	}
	if secondary.calls != 0 {
		t.Fatalf("secondary.calls = %d, want 0 (secondary must not run when primary answers)", secondary.calls)
	}
}

// TestFallbackConsultsSecondaryOnNotFound confirms the staged-cutover case: a
// reference primary's lookup table does not (yet) list is ErrNotFound, and
// Fallback reads it from secondary unchanged, exactly as before any cutover.
func TestFallbackConsultsSecondaryOnNotFound(t *testing.T) {
	primary := &fakeStore{values: map[Ref]string{}} // "a" not mapped -> ErrNotFound
	secondary := &fakeStore{values: map[Ref]string{"a": "old-value"}}
	f := NewFallback(primary, secondary)

	data, err := Resolve(context.Background(), f, "a")
	if err != nil || string(data) != "old-value" {
		t.Fatalf("Resolve = (%q, %v), want (\"old-value\", nil)", data, err)
	}
	if secondary.calls != 1 {
		t.Fatalf("secondary.calls = %d, want 1", secondary.calls)
	}

	// secondary itself may report not-found too (the reference is genuinely
	// absent from both stores); that error is returned unchanged.
	_, err = Resolve(context.Background(), f, "missing")
	if !IsNotFound(err) {
		t.Fatalf("both stores miss: err = %v, want ErrNotFound", err)
	}
}

// TestFallbackDoesNotConsultSecondaryOnNonNotFound is the pin for this type's
// entire reason to exist: a primary that is locked, unreadable, invalid or
// otherwise broken for a reference it DOES map must fail loudly. Falling
// back to secondary's possibly stale copy in this case is exactly the silent
// stale-fallback bug Fallback must not have (see docs/secrets.md and task
// 262's self-review). secondary.calls == 0 proves it was never even asked,
// not merely that its answer was discarded.
func TestFallbackDoesNotConsultSecondaryOnNonNotFound(t *testing.T) {
	for _, kind := range []error{ErrInvalid, ErrUnreadable, ErrUnavailable} {
		kind := kind
		t.Run(kind.Error(), func(t *testing.T) {
			primary := &fakeStore{fail: &Error{Kind: kind, Ref: "a", Msg: "typed"}}
			secondary := &fakeStore{values: map[Ref]string{"a": "old-value"}}
			f := NewFallback(primary, secondary)

			_, err := Resolve(context.Background(), f, "a")
			if KindOf(err) != kind {
				t.Fatalf("Resolve err = %v, want kind %v", err, kind)
			}
			if secondary.calls != 0 {
				t.Fatalf("secondary.calls = %d, want 0: a locked/broken primary must never fall back", secondary.calls)
			}
		})
	}
}

// TestFallbackUnclassifiedPrimaryErrorDoesNotFallBack covers the same
// property for an error Resolve itself turns into ErrUnavailable (a plain
// error, not a typed *Error): still no fallback.
func TestFallbackUnclassifiedPrimaryErrorDoesNotFallBack(t *testing.T) {
	primary := &fakeStore{fail: errors.New("boom")}
	secondary := &fakeStore{values: map[Ref]string{"a": "old-value"}}
	f := NewFallback(primary, secondary)

	_, err := Resolve(context.Background(), f, "a")
	if KindOf(err) != ErrUnavailable {
		t.Fatalf("Resolve err = %v, want ErrUnavailable", err)
	}
	if secondary.calls != 0 {
		t.Fatalf("secondary.calls = %d, want 0", secondary.calls)
	}
}

// TestFallbackPropagatesCancellation confirms a context that is already done
// is never a suppressible not-found (it carries no *Error kind at all), so
// it must not trigger a secondary read either.
func TestFallbackPropagatesCancellation(t *testing.T) {
	primary := &fakeStore{values: map[Ref]string{}}
	secondary := &fakeStore{values: map[Ref]string{"a": "old-value"}}
	f := NewFallback(primary, secondary)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Resolve(ctx, f, "a")
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("Resolve err = %v, want context.Canceled", err)
	}
	if KindOf(err) != nil {
		t.Fatalf("cancellation err has kind %v, want none (it must never look like ErrNotFound)", KindOf(err))
	}
	if secondary.calls != 0 {
		t.Fatalf("secondary.calls = %d, want 0", secondary.calls)
	}
}
