package dir

import (
	"reflect"
	"testing"

	"github.com/snonux/gonf/resource"
)

// TestSyncPayloadClone pins SyncPayload's Clone: it has no reference
// fields, so a clone must equal its source with no shared-storage check
// needed (a plain value has nothing to alias).
func TestSyncPayloadClone(t *testing.T) {
	p := SyncPayload{SourceDir: "/src", SourceGlob: "/src/*", FileMode: "0640"}
	if got := p.Clone(); !reflect.DeepEqual(got, resource.DraftPayload(p)) {
		t.Fatalf("clone = %#v, want %#v", got, p)
	}
}

// TestSyncPayloadSourceDirGlob pins that SourceDirGlob returns exactly the
// fields it was constructed with, and that SyncPayload satisfies
// resource.SourceDirPayload (checked by the assignment below), since
// api/packager.go and internal/testapply find it only through that
// kind-neutral interface, never by importing this package.
func TestSyncPayloadSourceDirGlob(t *testing.T) {
	var _ resource.SourceDirPayload = SyncPayload{}
	p := SyncPayload{SourceDir: "/src", SourceGlob: "/src/*"}
	gotDir, gotGlob := p.SourceDirGlob()
	if gotDir != "/src" || gotGlob != "/src/*" {
		t.Fatalf("SourceDirGlob() = (%q, %q), want (/src, /src/*)", gotDir, gotGlob)
	}
}
