package secret

import (
	"context"
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
