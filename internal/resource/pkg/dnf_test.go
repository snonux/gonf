package pkg

import (
	"os"
	"testing"

	"codeberg.org/snonux/gonf/internal/resource/embed"
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
		p.Absent = false
		p.latest = false
		if err := applyDNF(p); err != nil {
			t.Errorf("applyDNF Present failed: %v", err)
		}
	})

	t.Run("Latest", func(t *testing.T) {
		p.Absent = false
		p.latest = true
		if err := applyDNF(p); err != nil {
			t.Errorf("applyDNF Latest failed: %v", err)
		}
	})

	t.Run("Absent", func(t *testing.T) {
		p.Absent = true
		p.latest = false
		if err := applyDNF(p); err != nil {
			t.Errorf("applyDNF Absent failed: %v", err)
		}
	})

	t.Run("NonExistentPresent", func(t *testing.T) {
		pErr := &Package{
			name:    "non-existent-package-gonf-12345",
			Absence: embed.Absence{Absent: false},
			latest:  false,
		}
		if err := applyDNF(pErr); err == nil {
			t.Error("applyDNF Present should have failed for non-existent package")
		}
	})

	t.Run("NonExistentLatest", func(t *testing.T) {
		pErr := &Package{
			name:    "non-existent-package-gonf-12345",
			Absence: embed.Absence{Absent: false},
			latest:  true,
		}
		if err := applyDNF(pErr); err == nil {
			t.Error("applyDNF Latest should have failed for non-existent package")
		}
	})

	t.Run("NonExistentAbsent", func(t *testing.T) {
		pErr := &Package{
			name:    "non-existent-package-gonf-12345",
			Absence: embed.Absence{Absent: true},
			latest:  false,
		}
		// dnf remove is typically idempotent; removing a non-existent package should not error.
		if err := applyDNF(pErr); err != nil {
			t.Errorf("applyDNF Absent should be idempotent for non-existent package, but got error: %v", err)
		}
	})
}
