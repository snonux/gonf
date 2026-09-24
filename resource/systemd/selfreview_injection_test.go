package systemd

import (
	"testing"

	"github.com/snonux/gonf/internal/runners"
)

// TestSelfReviewTwoContextsDifferentSystemdRunnersNoSharedState is task
// 4e2's self-review probe (mirroring qb2's own plan/apply_runners_test.go):
// two Clients built from two independently constructed *runners.Set values
// (as two concurrent-in-spirit applies would each build their own) must
// never observe each other's fake, proving there is no process-global
// systemctl seam left in this package. Against pre-4e2 code this does not
// even compile: runners.SystemdRunners and NewClient do not exist yet, so
// the only way to fake systemctl was the process-global
// internal/testseam.FakeSystemctl this task removes (verified by hand: see
// the task's ask annotation for the revert-and-retest that pinned this).
func TestSelfReviewTwoContextsDifferentSystemdRunnersNoSharedState(t *testing.T) {
	var firstSaw, secondSaw string
	first := NewClient(&runners.SystemdRunners{Run: func(name string, args ...string) (string, string, int, error) {
		firstSaw = "first"
		return "", "", 0, nil
	}})
	second := NewClient(&runners.SystemdRunners{Run: func(name string, args ...string) (string, string, int, error) {
		secondSaw = "second"
		return "", "", 0, nil
	}})

	if err := second.Run("daemon-reload"); err != nil {
		t.Fatalf("second.Run: %v", err)
	}
	if err := first.Run("daemon-reload"); err != nil {
		t.Fatalf("first.Run: %v", err)
	}
	if firstSaw != "first" || secondSaw != "second" {
		t.Fatalf("cross-talk between independently built Clients: firstSaw=%q secondSaw=%q", firstSaw, secondSaw)
	}
}
