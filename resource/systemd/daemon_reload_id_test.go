package systemd

import (
	"testing"

	"github.com/snonux/gonf/resource"
	opt "github.com/snonux/gonf/resource/options"
)

// TestDaemonReloadIDMatchesItsRegistration pins task 272's ID agreement: the
// ID a daemon-reload reports and gates under at apply time (id) must be the
// one Present registers, for both the system and the user manager.
func TestDaemonReloadIDMatchesItsRegistration(t *testing.T) {
	resource.ResetForTest()
	t.Cleanup(resource.ResetForTest)
	for _, user := range []bool{false, true} {
		var opts []opt.DaemonReloadOption
		if user {
			opts = append(opts, opt.WithUser)
		}
		registered := Present(opts...).ID()
		if got := (&DaemonReloadResource{user: user}).id(); got != registered {
			t.Errorf("user=%v: apply-time id %q, registered %q", user, got, registered)
		}
	}
}
