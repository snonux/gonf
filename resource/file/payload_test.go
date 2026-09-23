package file

import (
	"testing"

	"github.com/snonux/gonf/resource"
)

// TestPayloadSourceFilePath pins that SourceFilePath returns exactly
// SourcePath, and that Payload satisfies resource.SourceFilePayload
// (checked by the assignment below), since api/packager.go and
// internal/testapply find it only through that kind-neutral interface,
// never by importing this package. Payload's Clone contract itself is
// covered generically, alongside every other resource.DraftPayload
// implementation, by resource.TestPayloadCloneContract and
// resource.TestPayloadClonePreservesNilAndEmpty
// (resource/payload_clone_test.go, task 1e2) — this file no longer
// hand-maintains its own fullPayload() fixture for that.
func TestPayloadSourceFilePath(t *testing.T) {
	var _ resource.SourceFilePayload = Payload{}
	p := Payload{SourcePath: "/src/x.conf"}
	if got := p.SourceFilePath(); got != "/src/x.conf" {
		t.Fatalf("SourceFilePath() = %q, want /src/x.conf", got)
	}
}
