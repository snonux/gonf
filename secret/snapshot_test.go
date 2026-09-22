package secret

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// formatVerbs are the fmt verbs a log line or debug print might use; each
// must print a Snapshot without its cached bytes (q82).
var formatVerbs = []string{"%v", "%+v", "%#v", "%s", "%q", "%d", "%x", "%X", "%o", "%T", "%p", "%t", "%10.3s", "%-8v"}

// A Snapshot holding a resolved secret formats without the secret, whether
// printed directly, as a nested field or inside a map, with any verb —
// including the bad-verb path (%s, %q) that fmt reprints at depth 0.
func TestSnapshotFormatRedactsCachedBytes(t *testing.T) {
	const value = "TOPSECRET"
	snap := NewSnapshot(ProviderFunc(func(context.Context, Ref) ([]byte, error) {
		return []byte(value), nil
	}))
	if _, err := snap.Resolve(context.Background(), "a"); err != nil {
		t.Fatal(err)
	}
	nested := struct{ P *Snapshot }{snap}
	forms := []string{value, fmt.Sprint([]byte(value)), fmt.Sprintf("%x", value), fmt.Sprintf("%X", value)}
	for _, verb := range formatVerbs {
		for _, arg := range []any{snap, nested, map[string]*Snapshot{"p": snap}, snap.entries["a"]} {
			out := fmt.Sprintf(verb, arg)
			for _, form := range forms {
				if strings.Contains(out, strings.Trim(form, "[]")) {
					t.Fatalf("Sprintf(%q, %T) = %q leaks the cached secret", verb, arg, out)
				}
			}
		}
	}
}

// Negative: the redacted form still identifies the value's type, so a debug
// print stays useful, and a nil or zero Snapshot formats without panicking.
func TestSnapshotFormatDescribesSnapshot(t *testing.T) {
	snap := NewSnapshot(&fakeStore{values: map[Ref]string{"a": "v", "b": "w"}})
	for _, ref := range []Ref{"a", "b"} {
		if _, err := snap.Resolve(context.Background(), ref); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name string
		arg  any
		want string
	}{
		{"resolved", snap, "secret.Snapshot{provider: *secret.fakeStore, entries: 2}"},
		{"nil", (*Snapshot)(nil), "secret.Snapshot(nil)"},
		{"zero", &Snapshot{}, "secret.Snapshot{provider: <nil>, entries: 0}"},
		{"entry", snap.entries["a"], "secret.snapshotEntry(redacted)"},
	} {
		if got := fmt.Sprint(tc.arg); got != tc.want {
			t.Errorf("%s: Sprint = %q, want %q", tc.name, got, tc.want)
		}
	}
	if got := fmt.Sprintf("%T", snap); got != "*secret.Snapshot" {
		t.Fatalf("%%T = %q", got)
	}
}

// exactStore is a Provider that matches references literally, without the
// lexical cleaning FileProvider applies, and records what it was asked.
type exactStore struct {
	values map[Ref]string
	asked  []Ref
}

func (s *exactStore) Resolve(_ context.Context, ref Ref) ([]byte, error) {
	s.asked = append(s.asked, ref)
	if v, ok := s.values[ref]; ok {
		return []byte(v), nil
	}
	return nil, &Error{Kind: ErrNotFound, Ref: ref, Msg: fmt.Sprintf("secret %q is missing", string(ref))}
}

// The provider behind a Snapshot is asked for the canonical key, so the
// first caller's spelling cannot poison the entry every spelling shares: a
// literal-matching provider finds "b" even when "x/../b" is asked first,
// and a later "b" is served that value, not a cached not-found (s82).
func TestSnapshotResolvesCanonicalKey(t *testing.T) {
	store := &exactStore{values: map[Ref]string{"b": "v"}}
	snap := NewSnapshot(store)
	for _, ref := range []Ref{"x/../b", "b", "/b"} {
		if data, err := Resolve(context.Background(), snap, ref); err != nil || string(data) != "v" {
			t.Fatalf("Resolve(%q) = (%q, %v), want v", ref, data, err)
		}
	}
	if len(store.asked) != 1 || store.asked[0] != "b" {
		t.Fatalf("provider asked %q, want exactly [b]", store.asked)
	}
}

// Negative: a not-found for the canonical key still names the caller's own
// spelling — for the resolving caller and for later ones — in Ref and in
// the message, so Resolve passes it through as not-found.
func TestSnapshotCanonicalNotFoundNamesCallerSpelling(t *testing.T) {
	store := &exactStore{}
	snap := NewSnapshot(store)
	for _, ref := range []Ref{"/a//m", "a/m", "./a/m"} {
		_, err := Resolve(context.Background(), snap, ref)
		want := fmt.Sprintf("secret %q is missing", string(ref))
		if !IsNotFound(err) || err.(*Error).Ref != ref || err.Error() != want {
			t.Fatalf("Resolve(%q) = %#v, want not-found %q", ref, err, want)
		}
	}
	if len(store.asked) != 1 || store.asked[0] != "a/m" {
		t.Fatalf("provider asked %q, want exactly [a/m]", store.asked)
	}
}

// Negative: a transient failure and a cancellation of the resolving caller
// also name its spelling, not the canonical key.
func TestSnapshotCanonicalFailuresNameCallerSpelling(t *testing.T) {
	snap := NewSnapshot(ProviderFunc(func(context.Context, Ref) ([]byte, error) {
		return nil, errors.New("store locked")
	}))
	_, err := Resolve(context.Background(), snap, "/a")
	if KindOf(err) != ErrUnavailable || err.(*Error).Ref != "/a" || !strings.Contains(err.Error(), `"/a"`) {
		t.Fatalf("transient failure = %v, want ErrUnavailable naming /a", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	snap = NewSnapshot(ProviderFunc(func(context.Context, Ref) ([]byte, error) {
		cancel()
		return []byte("v"), nil
	}))
	_, err = snap.Resolve(ctx, "/a")
	if !errors.Is(err, context.Canceled) || KindOf(err) != nil || !strings.Contains(err.Error(), `"/a"`) {
		t.Fatalf("cancelled = %v, want context.Canceled naming /a", err)
	}
}
