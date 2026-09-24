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
// stale-fallback bug Fallback must not have (see docs/design/secrets.md and task
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

// TestNewFallbackRefusesNilOperand confirms the fix for the confirmed
// panic-risk bug (task of2, following on task 262): NewFallback used to do no
// nil check at all, so a nil primary or secondary panicked on the first
// resolution — including for a TYPED nil (e.g. a nil *fakeStore wrapped in
// the Provider interface, the shape a swallowed constructor error or an
// unwired feature-flagged provider actually produces, not just a literal nil
// argument). NewFallback itself must never panic; Resolve on the broken
// result must return a typed ErrUnavailable instead of crashing.
func TestNewFallbackRefusesNilOperand(t *testing.T) {
	good := &fakeStore{values: map[Ref]string{"a": "v"}}
	for name, tc := range map[string]struct{ primary, secondary Provider }{
		"untyped nil primary":   {nil, good},
		"untyped nil secondary": {good, nil},
		"typed nil primary":     {(*fakeStore)(nil), good},
		"typed nil secondary":   {good, (*fakeStore)(nil)},
		"both nil":              {nil, nil},
	} {
		t.Run(name, func(t *testing.T) {
			f := NewFallback(tc.primary, tc.secondary)
			if f == nil {
				t.Fatal("NewFallback returned nil")
			}
			data, err := f.Resolve(context.Background(), "a")
			if data != nil {
				t.Fatalf("Resolve returned data %q despite a nil operand", data)
			}
			if KindOf(err) != ErrUnavailable {
				t.Fatalf("Resolve err = %v, want ErrUnavailable (not a panic)", err)
			}
		})
	}
}

// TestIsNilProviderRecursesIntoFallback confirms IsNilProvider recurses into
// *Fallback exactly as it does into *Snapshot, so api.SetSecretProvider can
// refuse a broken Fallback at the composition root (it cannot recurse into
// an unexported struct, which was the second half of the bug: IsNilProvider
// used to report false even for a Fallback that would panic on first use).
func TestIsNilProviderRecursesIntoFallback(t *testing.T) {
	good := &fakeStore{values: map[Ref]string{"a": "v"}}
	broken := []Provider{
		NewFallback(nil, good),
		NewFallback(good, nil),
		NewFallback((*fakeStore)(nil), good),
		NewFallback(good, (*fakeStore)(nil)),
		NewFallback(nil, nil),
	}
	for _, p := range broken {
		if !IsNilProvider(p) {
			t.Fatalf("IsNilProvider(%#v) = false, want true", p)
		}
	}
	if IsNilProvider(NewFallback(good, good)) {
		t.Fatal("IsNilProvider(NewFallback(good, good)) = true, want false")
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
