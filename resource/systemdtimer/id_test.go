package systemdtimer

import (
	"strings"
	"testing"

	"github.com/snonux/gonf/resource"
	opt "github.com/snonux/gonf/resource/options"
	"github.com/snonux/gonf/resource/systemd"
	"github.com/snonux/gonf/resource/timer"
)

// TestWatchedIDsMatchTheWatchedResources pins task 272's ID agreement: the
// composite decides it changed through AnyChanged on the IDs of the timer
// unit and the daemon-reload it applies, so those IDs must be exactly the
// ones resource/timer and resource/systemd register (and report under).
func TestWatchedIDsMatchTheWatchedResources(t *testing.T) {
	resource.ResetForTest()
	t.Cleanup(resource.ResetForTest)
	st := newTimer("backup", opt.WithCommand("/bin/true"))
	if got, want := timerUnitID(st.name), timer.Present(st.name).ID(); got != want {
		t.Errorf("timerUnitID(%q) = %q, registered Timer ID %q", st.name, got, want)
	}
	if got, want := daemonReloadID(false), systemd.Present().ID(); got != want {
		t.Errorf("daemonReloadID(system) = %q, registered %q", got, want)
	}
	if got, want := daemonReloadID(true), systemd.Present(opt.WithUser).ID(); got != want {
		t.Errorf("daemonReloadID(user) = %q, registered %q", got, want)
	}
}

// TestApplyReportsUnderTheRegisteredID observes the composite's apply-time
// ID through a validation failure (no command), which is prefixed with it.
func TestApplyReportsUnderTheRegisteredID(t *testing.T) {
	resource.ResetForTest()
	t.Cleanup(resource.ResetForTest)
	registered := Present("backup").ID()
	err := Ensure("backup")
	if err == nil || !strings.HasPrefix(err.Error(), registered+": ") {
		t.Fatalf("Ensure() = %v, want it prefixed with the registered ID %q", err, registered)
	}
}
