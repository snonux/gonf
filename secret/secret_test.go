package secret

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
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

// KindOf reads only a top-level *Error: an unreadable store whose cause
// wraps a not-found, or any wrapped typed error, must not look optional.
func TestKindOfReadsOnlyTopLevelError(t *testing.T) {
	inner := &Error{Kind: ErrNotFound, Ref: "inner"}
	unreadable := &Error{Kind: ErrUnreadable, Ref: "outer", Err: inner}
	if KindOf(unreadable) != ErrUnreadable || IsNotFound(unreadable) {
		t.Fatalf("KindOf = %v, IsNotFound = %v, want ErrUnreadable/false", KindOf(unreadable), IsNotFound(unreadable))
	}
	for _, wrapped := range []error{
		fmt.Errorf("context: %w", unreadable),
		fmt.Errorf("context: %w", inner),
	} {
		if KindOf(wrapped) != nil || IsNotFound(wrapped) {
			t.Fatalf("KindOf(%v) = %v, want nil for a wrapped error", wrapped, KindOf(wrapped))
		}
	}
	if KindOf(errors.New("plain")) != nil || KindOf(nil) != nil {
		t.Fatal("KindOf of a non-secret error must be nil")
	}
}

// The review probe: a provider failing to unlock its store with a typed
// not-found about ANOTHER reference (its password file), wrapped or not,
// must read as ErrUnavailable for the requested secret — never not-found.
func TestResolveRefusesForeignOrWrappedNotFound(t *testing.T) {
	unlock := &Error{Kind: ErrNotFound, Ref: "unlock/password-file"}
	for name, fail := range map[string]error{
		"wrapped foreign": fmt.Errorf("unlock store: %w", unlock),
		"bare foreign":    unlock,
		"wrapped own":     fmt.Errorf("lookup: %w", &Error{Kind: ErrNotFound, Ref: "r"}),
	} {
		_, err := Resolve(context.Background(), &fakeStore{fail: fail}, "r")
		if KindOf(err) != ErrUnavailable || IsNotFound(err) {
			t.Fatalf("%s: err = %v (kind %v), want ErrUnavailable", name, err, KindOf(err))
		}
		if e := err.(*Error); e.Ref != "r" || !errors.Is(err, fail) {
			t.Fatalf("%s: err = %#v, want Ref r with the provider error as cause", name, e)
		}
	}
}

// A context error of the provider's own (e.g. its subprocess timeout) while
// the caller's ctx is still live is a store failure, not a cancellation the
// caller asked for.
func TestResolveTreatsProviderContextErrorAsUnavailable(t *testing.T) {
	for _, internal := range []error{context.DeadlineExceeded, context.Canceled,
		fmt.Errorf("foostore: %w", context.DeadlineExceeded)} {
		_, err := Resolve(context.Background(), &fakeStore{fail: internal}, "r")
		if KindOf(err) != ErrUnavailable || !errors.Is(err, internal) {
			t.Fatalf("provider %v: err = %v (kind %v), want ErrUnavailable", internal, err, KindOf(err))
		}
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

// A cache hit returns a copy too: scribbling on it changes nothing.
func TestSnapshotCopiesOnHit(t *testing.T) {
	snap := NewSnapshot(&fakeStore{values: map[Ref]string{"a": "v1"}})
	for i := range 3 {
		data, err := snap.Resolve(context.Background(), "a")
		if err != nil || string(data) != "v1" {
			t.Fatalf("resolution %d = (%q, %v), want v1", i, data, err)
		}
		data[0] = 'X'
	}
}

// The miss result is a copy too: a caller editing it cannot reach the
// provider's own buffer, which a provider may keep and hand out again.
// (z52 review 4.)
func TestSnapshotCopiesOnMiss(t *testing.T) {
	buf := []byte("v1")
	snap := NewSnapshot(ProviderFunc(func(context.Context, Ref) ([]byte, error) { return buf, nil }))
	data, err := snap.Resolve(context.Background(), "a")
	if err != nil || string(data) != "v1" {
		t.Fatalf("miss = (%q, %v), want v1", data, err)
	}
	data[0] = 'X'
	if string(buf) != "v1" {
		t.Fatalf("provider buffer = %q after editing the miss result, want v1", buf)
	}
}

// Spellings FileProvider treats alike share one entry, and a cached
// not-found still reads as not-found through Resolve for another spelling
// (its Ref is rewritten to the requested one).
func TestSnapshotCanonicalisesRefs(t *testing.T) {
	store := &fakeStore{values: map[Ref]string{"a/b": "v"}}
	snap := NewSnapshot(store)
	for _, ref := range []Ref{"a/b", "/a/b", `\a/b`, `/\a/b`, "a//b", "./a/b", "a/x/../b"} {
		if data, err := Resolve(context.Background(), snap, ref); err != nil || string(data) != "v" {
			t.Fatalf("Resolve(%q) = (%q, %v)", ref, data, err)
		}
	}
	for _, ref := range []Ref{"m", "/m", "m/."} {
		_, err := Resolve(context.Background(), snap, ref)
		if !IsNotFound(err) || err.(*Error).Ref != ref {
			t.Fatalf("Resolve(%q) = %#v, want not-found naming %q", ref, err, ref)
		}
	}
	if store.calls != 2 {
		t.Fatalf("calls = %d, want 2 (one per canonical reference)", store.calls)
	}
	// Only leading backslashes are stripped: in `a\b` it is part of the
	// name, a reference of its own (z52 review 4).
	if _, err := Resolve(context.Background(), snap, `a\b`); !IsNotFound(err) || store.calls != 3 {
		t.Fatalf(`Resolve("a\\b") = %v after %d calls, want its own not-found lookup`, err, store.calls)
	}
	// No canonical form: passed through uncached every time.
	for range 2 {
		_, _ = snap.Resolve(context.Background(), "")
	}
	if store.calls != 5 {
		t.Fatalf("calls = %d, want 5 (empty reference uncached)", store.calls)
	}
}

// One slow reference does not block another, and a caller waiting for a
// slow resolution of the same reference gives up with its own ctx.
func TestSnapshotDoesNotSerialiseReferences(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	provider := ProviderFunc(func(_ context.Context, ref Ref) ([]byte, error) {
		if ref == "slow" {
			close(entered)
			<-release
		}
		return []byte("v-" + string(ref)), nil
	})
	snap := NewSnapshot(provider)
	leader := make(chan error, 1)
	go func() {
		_, err := snap.Resolve(context.Background(), "slow")
		leader <- err
	}()
	<-entered

	bounded, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if data, err := snap.Resolve(bounded, "fast"); err != nil || string(data) != "v-fast" {
		t.Fatalf("fast while slow in flight = (%q, %v)", data, err)
	}

	waiter, cancelWaiter := context.WithCancel(context.Background())
	cancelWaiter()
	if _, err := snap.Resolve(waiter, "slow"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled waiter = %v, want context.Canceled", err)
	}

	close(release)
	if err := <-leader; err != nil {
		t.Fatalf("leader: %v", err)
	}
	if data, err := snap.Resolve(context.Background(), "slow"); err != nil || string(data) != "v-slow" {
		t.Fatalf("slow after release = (%q, %v)", data, err)
	}
}

// parkedWaiters installs the onWait test hook and returns a channel that
// receives once per caller that found an in-flight entry and is waiting.
func parkedWaiters(snap *Snapshot) <-chan Ref {
	parked := make(chan Ref, 64)
	snap.onWait = func(key Ref) { parked <- key }
	return parked
}

// Concurrent callers of one reference share one provider call: every
// non-leading caller is parked on the leader's entry before it is released,
// so the waiter path is always exercised.
func TestSnapshotResolvesConcurrentCallersOnce(t *testing.T) {
	const callers = 8
	var calls atomic.Int32
	entered, release := make(chan struct{}), make(chan struct{})
	snap := NewSnapshot(ProviderFunc(func(context.Context, Ref) ([]byte, error) {
		calls.Add(1)
		close(entered)
		<-release
		return []byte("v"), nil
	}))
	parked := parkedWaiters(snap)
	var wg sync.WaitGroup
	for range callers {
		wg.Go(func() {
			if data, err := snap.Resolve(context.Background(), "k"); err != nil || string(data) != "v" {
				t.Errorf("Resolve = (%q, %v)", data, err)
			}
		})
	}
	<-entered
	for range callers - 1 {
		<-parked
	}
	close(release)
	wg.Wait()
	if n := calls.Load(); n != 1 {
		t.Fatalf("provider calls = %d, want 1", n)
	}
}

// resolveAsync runs snap.Resolve(ctx, ref) on a goroutine and returns a
// channel with its result.
func resolveAsync(ctx context.Context, snap *Snapshot, ref Ref) <-chan result {
	out := make(chan result, 1)
	go func() {
		data, err := snap.Resolve(ctx, ref)
		out <- result{data, err}
	}()
	return out
}

type result struct {
	data []byte
	err  error
}

// awaitResult fails the test when a resolution does not finish in time.
func awaitResult(t *testing.T, ch <-chan result) result {
	t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(5 * time.Second):
		t.Fatal("resolution hung")
		return result{}
	}
}

// A waiter parked on a leader whose resolution fails transiently (the
// leader's own ctx is cancelled) retries with its own live ctx and gets the
// value — not the leader's error, and not (nil, nil). (From the second z52
// review, which found this path untested.)
func TestSnapshotWaiterRetriesAfterLeaderFailure(t *testing.T) {
	var calls atomic.Int32
	entered, release := make(chan struct{}), make(chan struct{})
	snap := NewSnapshot(ProviderFunc(func(ctx context.Context, ref Ref) ([]byte, error) {
		if calls.Add(1) == 1 {
			close(entered)
			<-release
			return nil, ctx.Err()
		}
		return []byte("v"), nil
	}))
	parked := parkedWaiters(snap)
	lctx, lcancel := context.WithCancel(context.Background())
	leader := resolveAsync(lctx, snap, "k")
	<-entered
	waiter := resolveAsync(context.Background(), snap, "k")
	<-parked
	lcancel()
	close(release)
	if r := awaitResult(t, leader); !errors.Is(r.err, context.Canceled) {
		t.Fatalf("leader = (%q, %v), want context.Canceled", r.data, r.err)
	}
	if r := awaitResult(t, waiter); r.err != nil || string(r.data) != "v" {
		t.Fatalf("waiter = (%q, %v), want v after retry", r.data, r.err)
	}
	if n := calls.Load(); n != 2 {
		t.Fatalf("provider calls = %d, want 2 (failed leader + retry)", n)
	}
}

// A panicking provider must not leave a parked waiter hanging (the entry
// cleanup runs in a defer), and the waiter's retry resolves the value.
// (From the second z52 review.)
func TestSnapshotPanickingLeaderReleasesWaiters(t *testing.T) {
	var calls atomic.Int32
	entered, release := make(chan struct{}), make(chan struct{})
	snap := NewSnapshot(ProviderFunc(func(context.Context, Ref) ([]byte, error) {
		if calls.Add(1) == 1 {
			close(entered)
			<-release
			panic("provider bug")
		}
		return []byte("v"), nil
	}))
	parked := parkedWaiters(snap)
	panicked := make(chan any, 1)
	go func() {
		defer func() { panicked <- recover() }()
		_, _ = snap.Resolve(context.Background(), "k")
	}()
	<-entered
	waiter := resolveAsync(context.Background(), snap, "k")
	<-parked
	close(release)
	if p := <-panicked; p == nil {
		t.Fatal("leader did not panic")
	}
	if r := awaitResult(t, waiter); r.err != nil || string(r.data) != "v" {
		t.Fatalf("waiter after panic = (%q, %v), want v", r.data, r.err)
	}
}

// A nil provider is a composition-root programmer error: NewSnapshot panics
// immediately, not at the first resolution — including a typed nil pointer.
func TestNewSnapshotRefusesNilProvider(t *testing.T) {
	for name, p := range map[string]Provider{
		"untyped":       nil,
		"typed nil":     (*fakeStore)(nil),
		"zero snapshot": &Snapshot{},
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				r := recover()
				if r == nil || !strings.Contains(fmt.Sprint(r), "nil provider") {
					t.Fatalf("NewSnapshot panic = %v, want the nil-provider refusal", r)
				}
			}()
			_ = NewSnapshot(p)
		})
	}
}

func TestIsNilProvider(t *testing.T) {
	for _, nilP := range []Provider{nil, (*fakeStore)(nil), ProviderFunc(nil), (*Snapshot)(nil), &Snapshot{}} {
		if !IsNilProvider(nilP) {
			t.Fatalf("IsNilProvider(%#v) = false, want true", nilP)
		}
	}
	for _, okP := range []Provider{&fakeStore{}, FileProvider{}, NewSnapshot(FileProvider{}), ProviderFunc(func(context.Context, Ref) ([]byte, error) { return nil, nil })} {
		if IsNilProvider(okP) {
			t.Fatalf("IsNilProvider(%#v) = true, want false", okP)
		}
	}
}

// Negative: none of the contract's own failure paths — cancellation after a
// provider produced bytes, classification of an unclassified or foreign
// error, or a Snapshot hit on a cached not-found under another spelling —
// puts the value a provider handed back into an
// error. (A provider that writes the value into its own error text breaks
// its contract; that cannot be detected here.)
func TestContractErrorsNeverContainValues(t *testing.T) {
	const value = "never-report-this-secret"
	check := func(name string, err error) {
		t.Helper()
		if err == nil || strings.Contains(err.Error(), value) || strings.Contains(fmt.Sprintf("%#v", err), value) {
			t.Fatalf("%s: err = %v", name, err)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	leaky := ProviderFunc(func(context.Context, Ref) ([]byte, error) {
		cancel()
		return []byte(value), nil
	})
	_, err := Resolve(ctx, leaky, "a")
	check("cancel after bytes", err)

	valueThenFail := ProviderFunc(func(context.Context, Ref) ([]byte, error) {
		return []byte(value), errors.New("store locked")
	})
	data, err := Resolve(context.Background(), valueThenFail, "a")
	check("bytes with an unclassified error", err)
	if data != nil {
		t.Fatalf("Resolve returned bytes with an error: %q", data)
	}

	snap := NewSnapshot(&fakeStore{values: map[Ref]string{"x": value}})
	_, _ = snap.Resolve(context.Background(), "missing")
	_, err = snap.Resolve(context.Background(), "/missing")
	check("snapshot cached not-found", err)
}

// Negative: a zero Snapshot used directly (not through NewSnapshot, and past
// SetSecretProvider's refusal) fails each resolution with a typed
// ErrUnavailable instead of panicking on its nil provider or map.
func TestZeroSnapshotIsUnavailable(t *testing.T) {
	var snap Snapshot
	for _, ref := range []Ref{"k", ""} {
		data, err := snap.Resolve(context.Background(), ref)
		if data != nil || KindOf(err) != ErrUnavailable || !strings.Contains(err.Error(), "NewSnapshot") {
			t.Fatalf("zero Snapshot Resolve(%q) = (%q, %v), want ErrUnavailable", ref, data, err)
		}
	}
}

// A cached not-found served for another spelling names the requested
// reference in Ref and in its message, exactly as a direct lookup of that
// spelling would word it; a message that does not quote the reference is
// kept as it is.
func TestSnapshotCachedNotFoundMessageNamesRequestedRef(t *testing.T) {
	var calls int
	snap := NewSnapshot(ProviderFunc(func(_ context.Context, ref Ref) ([]byte, error) {
		calls++
		if ref == "plain" {
			return nil, &Error{Kind: ErrNotFound, Ref: ref, Msg: "no such secret"}
		}
		return nil, &Error{Kind: ErrNotFound, Ref: ref, Msg: fmt.Sprintf("secret %q is missing", string(ref))}
	}))
	for _, ref := range []Ref{"a/m", "/a/m", "a//m"} {
		_, err := Resolve(context.Background(), snap, ref)
		if want := fmt.Sprintf("secret %q is missing", string(ref)); !IsNotFound(err) || err.(*Error).Ref != ref || err.Error() != want {
			t.Fatalf("Resolve(%q) = %v, want not-found %q", ref, err, want)
		}
	}
	_, _ = snap.Resolve(context.Background(), "plain")
	if _, err := snap.Resolve(context.Background(), "/plain"); err.Error() != "no such secret" || err.(*Error).Ref != "/plain" {
		t.Fatalf("Resolve(/plain) = %#v, want the unchanged message naming /plain", err)
	}
	if calls != 2 {
		t.Fatalf("provider calls = %d, want 2 (one per canonical reference)", calls)
	}
}
