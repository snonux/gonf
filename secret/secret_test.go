package secret

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"testing"
)

// fakeStore is an in-memory Provider with synthetic values that counts calls
// and can be told to fail.
type fakeStore struct {
	values map[Ref]string
	fail   error // returned instead of a lookup when set
	calls  int
}

func (f *fakeStore) Resolve(ctx context.Context, ref Ref) ([]byte, error) {
	f.calls++
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if f.fail != nil {
		return nil, f.fail
	}
	v, ok := f.values[ref]
	if !ok {
		return nil, &Error{Kind: ErrNotFound, Ref: ref}
	}
	return []byte(v), nil
}

func TestResolveReturnsProviderBytes(t *testing.T) {
	store := &fakeStore{values: map[Ref]string{"a": "synthetic\n"}}
	data, err := Resolve(context.Background(), store, "a")
	if err != nil || string(data) != "synthetic\n" {
		t.Fatalf("Resolve = (%q, %v)", data, err)
	}
	_, err = Resolve(context.Background(), store, "b")
	if !IsNotFound(err) || !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing ref: err = %v, want ErrNotFound", err)
	}
	if got := err.Error(); got != `secret "b": secret not found` {
		t.Fatalf("generated message = %q", got)
	}
}

// Typed errors of every kind pass through unchanged; anything else —
// including an *Error with an unknown kind, or a bare fs.ErrNotExist — is
// ErrUnavailable, so a provider can never be read as "not found" by accident.
func TestResolveClassifiesErrors(t *testing.T) {
	for _, kind := range kinds {
		want := &Error{Kind: kind, Ref: "r", Msg: "typed"}
		_, err := Resolve(context.Background(), &fakeStore{fail: want}, "r")
		if err != want || KindOf(err) != kind {
			t.Fatalf("typed %v: err = %v (kind %v), want pass-through", kind, err, KindOf(err))
		}
	}
	for _, raw := range []error{
		errors.New("store locked"),
		fs.ErrNotExist,
		&Error{Kind: errors.New("bogus"), Ref: "r"},
	} {
		_, err := Resolve(context.Background(), &fakeStore{fail: raw}, "r")
		if KindOf(err) != ErrUnavailable || IsNotFound(err) || !errors.Is(err, raw) {
			t.Fatalf("unclassified %v: err = %v (kind %v), want ErrUnavailable wrapping it", raw, err, KindOf(err))
		}
	}
}

// KindOf reads the outermost *Error: an unreadable store whose cause wraps a
// not-found must not look optional.
func TestKindOfUsesOutermostError(t *testing.T) {
	inner := &Error{Kind: ErrNotFound, Ref: "inner"}
	outer := fmt.Errorf("context: %w", &Error{Kind: ErrUnreadable, Ref: "outer", Err: inner})
	if KindOf(outer) != ErrUnreadable || IsNotFound(outer) {
		t.Fatalf("KindOf = %v, IsNotFound = %v, want ErrUnreadable/false", KindOf(outer), IsNotFound(outer))
	}
	if KindOf(errors.New("plain")) != nil || KindOf(nil) != nil {
		t.Fatal("KindOf of a non-secret error must be nil")
	}
}

func TestErrorMessageAndUnwrap(t *testing.T) {
	cause := errors.New("io failed")
	err := &Error{Kind: ErrUnreadable, Ref: "x/y", Err: cause}
	if got := err.Error(); got != `secret "x/y": secret unreadable: io failed` {
		t.Fatalf("Error() = %q", got)
	}
	if !errors.Is(err, ErrUnreadable) || !errors.Is(err, cause) {
		t.Fatal("Unwrap must expose kind and cause")
	}
	if got := (&Error{Kind: ErrInvalid, Msg: "custom"}).Error(); got != "custom" {
		t.Fatalf("Msg override = %q", got)
	}
}

func TestResolveHonoursCancellation(t *testing.T) {
	store := &fakeStore{values: map[Ref]string{"a": "synthetic"}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	data, err := Resolve(ctx, store, "a")
	if !errors.Is(err, context.Canceled) || data != nil || store.calls != 0 || KindOf(err) != nil {
		t.Fatalf("pre-cancelled: (%q, %v, calls %d), want Canceled without calling the provider", data, err, store.calls)
	}

	// A provider that ignores cancellation and returns bytes anyway: Resolve
	// still refuses to hand them out.
	ctx, cancel = context.WithCancel(context.Background())
	sloppy := ProviderFunc(func(context.Context, Ref) ([]byte, error) {
		cancel()
		return []byte("synthetic"), nil
	})
	data, err = Resolve(ctx, sloppy, "a")
	if !errors.Is(err, context.Canceled) || data != nil {
		t.Fatalf("cancel during provider: (%q, %v), want Canceled and no bytes", data, err)
	}

	ctx, cancel = context.WithTimeout(context.Background(), 0)
	defer cancel()
	if _, err := Resolve(ctx, store, "a"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expired deadline: err = %v", err)
	}
}

func TestSnapshotResolvesOnce(t *testing.T) {
	store := &fakeStore{values: map[Ref]string{"a": "v1"}}
	snap := NewSnapshot(store)
	first, err := snap.Resolve(context.Background(), "a")
	if err != nil || string(first) != "v1" {
		t.Fatalf("first = (%q, %v)", first, err)
	}
	store.values["a"] = "v2" // rotated mid-snapshot
	first[0] = 'X'           // caller scribbles on its copy
	second, err := snap.Resolve(context.Background(), "a")
	if err != nil || string(second) != "v1" || store.calls != 1 {
		t.Fatalf("second = (%q, %v, calls %d), want v1 from one provider call", second, err, store.calls)
	}
	// Not-found is a fact about the store and is remembered too.
	for range 2 {
		if _, err := snap.Resolve(context.Background(), "missing"); !IsNotFound(err) {
			t.Fatalf("missing: %v", err)
		}
	}
	if store.calls != 2 {
		t.Fatalf("calls = %d, want 2 (one per reference)", store.calls)
	}
}

// Transient failures (unavailable store, cancellation) are not cached.
func TestSnapshotRetriesTransientFailures(t *testing.T) {
	store := &fakeStore{values: map[Ref]string{"a": "v"}, fail: errors.New("locked")}
	snap := NewSnapshot(store)
	if _, err := snap.Resolve(context.Background(), "a"); KindOf(err) != ErrUnavailable {
		t.Fatalf("locked: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := snap.Resolve(ctx, "a"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled: %v", err)
	}
	store.fail = nil
	if data, err := snap.Resolve(context.Background(), "a"); err != nil || string(data) != "v" {
		t.Fatalf("after unlock = (%q, %v)", data, err)
	}
}

// A nil provider is a composition-root programmer error: NewSnapshot panics
// immediately, not at the first resolution.
func TestNewSnapshotRefusesNilProvider(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil || !strings.Contains(fmt.Sprint(r), "nil provider") {
			t.Fatalf("NewSnapshot(nil) panic = %v, want the nil-provider refusal", r)
		}
	}()
	_ = NewSnapshot(nil)
}

// Negative: no failure path of the contract puts the secret value into an
// error message.
func TestErrorsNeverContainValues(t *testing.T) {
	const value = "never-report-this-secret"
	ctx, cancel := context.WithCancel(context.Background())
	leaky := ProviderFunc(func(context.Context, Ref) ([]byte, error) {
		cancel()
		return []byte(value), nil
	})
	_, err := Resolve(ctx, leaky, "a")
	if err == nil || strings.Contains(err.Error(), value) {
		t.Fatalf("err = %v", err)
	}
}
