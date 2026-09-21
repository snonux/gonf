package cron

import (
	"strings"
	"testing"

	"github.com/snonux/gonf/resource"
	opt "github.com/snonux/gonf/resource/options"
)

// TestApplyReportsUnderTheRegisteredID pins task 272's ID agreement for the
// two-part cron name: the apply-time ID (observed through a validation
// failure, which is prefixed with it) is exactly the registered
// Cron[<user>/<name>] ID.
func TestApplyReportsUnderTheRegisteredID(t *testing.T) {
	resource.ResetForTest()
	t.Cleanup(resource.ResetForTest)
	registered := Present("backup", opt.WithCronUser("alice")).ID()
	if registered != "Cron[alice/backup]" {
		t.Fatalf("registered ID = %q, want Cron[alice/backup]", registered)
	}
	err := Ensure("backup", opt.WithCronUser("alice")) // no command: fails validation
	if err == nil || !strings.HasPrefix(err.Error(), registered+": ") {
		t.Fatalf("Ensure() = %v, want it prefixed with %q", err, registered)
	}
}
