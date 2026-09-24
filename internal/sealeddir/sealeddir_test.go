package sealeddir

import (
	"context"
	"testing"
)

// Resolve maps exactly the refs With named to the private dir; every other
// ref, an empty ref, and a context without an override keep planDir.
func TestResolve(t *testing.T) {
	ctx := With(context.Background(), "/private", []string{"blobs/a", "blobs/b"})
	for ref, want := range map[string]string{"blobs/a": "/private", "blobs/b": "/private", "blobs/c": "/sticky", "": "/sticky"} {
		if got := Resolve(ctx, ref, "/sticky"); got != want {
			t.Fatalf("Resolve(%q) = %q, want %q", ref, got, want)
		}
	}
	if got := Resolve(context.Background(), "blobs/a", "/sticky"); got != "/sticky" {
		t.Fatalf("no override: %q", got)
	}
	for _, c := range []context.Context{With(context.Background(), "", []string{"blobs/a"}), With(context.Background(), "/p", nil)} {
		if got := Resolve(c, "blobs/a", "/sticky"); got != "/sticky" {
			t.Fatalf("empty override resolved to %q", got)
		}
	}
}
