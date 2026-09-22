package secret

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// A provider that returns a nil *Error in a non-nil error interface (the
// classic nil-receiver mistake) gets ErrUnavailable from Resolve — directly
// and behind a Snapshot — instead of a nil-pointer panic (r82).
func TestResolveTreatsTypedNilErrorAsUnavailable(t *testing.T) {
	typedNil := ProviderFunc(func(context.Context, Ref) ([]byte, error) {
		var e *Error
		return nil, e
	})
	for name, p := range map[string]Provider{"direct": typedNil, "snapshot": NewSnapshot(typedNil)} {
		for range 2 { // the second call proves the snapshot did not cache it
			data, err := Resolve(context.Background(), p, "k")
			if data != nil || KindOf(err) != ErrUnavailable || IsNotFound(err) {
				t.Fatalf("%s: Resolve = (%q, %v), want ErrUnavailable", name, data, err)
			}
			if e := err.(*Error); e.Ref != "k" || e.Err != nil || !strings.Contains(err.Error(), "nil *secret.Error") {
				t.Fatalf("%s: error = %#v (%v), want one naming k and the nil *Error", name, e, err)
			}
		}
	}
}

// Negative: the helpers that inspect an error treat a nil *Error as no typed
// error at all rather than panicking, and a nil *Error's own methods are
// safe too.
func TestNilErrorHelpersDoNotPanic(t *testing.T) {
	var e *Error
	var err error = e
	if KindOf(err) != nil || IsNotFound(err) {
		t.Fatalf("KindOf(nil *Error) = %v, want nil", KindOf(err))
	}
	if got := e.Error(); got != "secret: nil *secret.Error" {
		t.Fatalf("Error() = %q", got)
	}
	if e.Unwrap() != nil || errors.Is(err, ErrNotFound) {
		t.Fatal("nil *Error unwraps to something")
	}
	if got := withRef(err, "k"); got != err {
		t.Fatalf("withRef(nil *Error) = %#v, want it unchanged", got)
	}
}
