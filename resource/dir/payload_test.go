package dir

import (
	"testing"

	"github.com/snonux/gonf/resource"
)

// TestSyncPayloadSourceDirGlob pins that SourceDirGlob returns exactly the
// fields it was constructed with, and that SyncPayload satisfies
// resource.SourceDirPayload (checked by the assignment below), since
// api/packager.go and internal/testapply find it only through that
// kind-neutral interface, never by importing this package. SyncPayload's
// Clone contract itself is covered generically, alongside every other
// resource.DraftPayload implementation, by resource.TestPayloadCloneContract
// and resource.TestPayloadClonePreservesNilAndEmpty
// (resource/payload_clone_test.go, task 1e2).
func TestSyncPayloadSourceDirGlob(t *testing.T) {
	var _ resource.SourceDirPayload = SyncPayload{}
	p := SyncPayload{SourceDir: "/src", SourceGlob: "/src/*"}
	gotDir, gotGlob := p.SourceDirGlob()
	if gotDir != "/src" || gotGlob != "/src/*" {
		t.Fatalf("SourceDirGlob() = (%q, %q), want (/src, /src/*)", gotDir, gotGlob)
	}
}
