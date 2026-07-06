package pkg

import (
	"os"
	"testing"
)

func TestApplyDNF(t *testing.T) {
	// Only run this test if explicitly enabled via environment variable.
	if os.Getenv("GONF_RUN_DNF_TESTS") != "1" {
		t.Skip("Skipping DNF test: GONF_RUN_DNF_TESTS=1 not set")
	}

	// Skip if not running as root, as dnf requires superuser privileges.
	if os.Getuid() != 0 {
		t.Skip("Skipping DNF test: root privileges required")
	}

	p := &Package{
		name: "tig",
	}

	t.Run("Present", func(t *testing.T) {
		p.absent = false
		p.latest = false
		if err := applyDNF(p); err != nil {
			t.Errorf("applyDNF Present failed: %v", err)
		}
	})

	t.Run("Latest", func(t *testing.T) {
		p.absent = false
		p.latest = true
		if err := applyDNF(p); err != nil {
			t.Errorf("applyDNF Latest failed: %v", err)
		}
	})

	t.Run("Absent", func(t *testing.T) {
		p.absent = true
		p.latest = false
		if err := applyDNF(p); err != nil {
			t.Errorf("applyDNF Absent failed: %v", err)
		}

		// // Restore the package so we don't leave the system in a changed state
		// p.absent = false
		// if err := applyDNF(p); err != nil {
		// 	t.Errorf("failed to restore package tig after Absent test: %v", err)
		// }
	})
}
