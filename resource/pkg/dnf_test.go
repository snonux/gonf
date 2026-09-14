package pkg

import (
	"bytes"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/snonux/gonf/resource"
	"github.com/snonux/gonf/resource/embed"
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
		// The rpm -q probe reports the package as not installed, so applyDNF
		// converges without ever running dnf remove (which is idempotent anyway).
		if err := applyDNF(pErr); err != nil {
			t.Errorf("applyDNF Absent should be idempotent for non-existent package, but got error: %v", err)
		}
	})
}

type dnfInvocation struct {
	name string
	args []string
}

// fakeDNFRunner simulates the rpm -q probe (via installed) and dnf,
// recording every invocation so tests can assert exactly which commands ran.
func fakeDNFRunner(installed bool, calls *[]dnfInvocation) func(string, ...string) (string, string, int, error) {
	return func(name string, args ...string) (string, string, int, error) {
		*calls = append(*calls, dnfInvocation{name: name, args: args})
		switch {
		case name == "rpm" && len(args) == 2 && args[0] == "-q":
			if installed {
				return args[1] + "-1.0-1.fc42.x86_64\n", "", 0, nil
			}
			return "", "package " + args[1] + " is not installed\n", 1, nil
		case name == "dnf":
			return "", "", 0, nil
		default:
			return "", "unexpected " + name, 1, nil
		}
	}
}

// TestApplyDNFFake pins the probe-then-act behavior of the dnf backend,
// mirroring the BSD backends: converged states note ok without running dnf,
// would-act states run dnf and note changed (would-change in dry-run).
func TestApplyDNFFake(t *testing.T) {
	oldRun, oldDry := runCmd, resource.DryRun()
	defer func() {
		runCmd = oldRun
		resource.SetDryRun(oldDry)
	}()

	withAbsent := func(p Package) Package {
		p.Absent = true
		return p
	}
	withLatest := func(p Package) Package {
		p.latest = true
		return p
	}

	tests := []struct {
		name      string
		pkg       Package
		installed bool
		dryRun    bool
		wantDNF   []string // expected dnf args; nil means dnf must not run
		wantNote  resource.Status
	}{
		{
			name:      "present converged when rpm reports installed",
			pkg:       Package{name: "rsync"},
			installed: true,
			wantNote:  resource.StatusOK,
		},
		{
			name:     "present installs when rpm reports not installed",
			pkg:      Package{name: "rsync"},
			wantDNF:  []string{"install", "-y", "rsync"},
			wantNote: resource.StatusChanged,
		},
		{
			name:     "absent converged when rpm reports not installed",
			pkg:      withAbsent(Package{name: "rsync"}),
			wantNote: resource.StatusOK,
		},
		{
			name:      "absent removes when rpm reports installed",
			pkg:       withAbsent(Package{name: "rsync"}),
			installed: true,
			wantDNF:   []string{"remove", "-y", "rsync"},
			wantNote:  resource.StatusChanged,
		},
		{
			name:     "latest updates when not installed",
			pkg:      withLatest(Package{name: "rsync"}),
			wantDNF:  []string{"update", "-y", "rsync"},
			wantNote: resource.StatusChanged,
		},
		{
			name:      "latest updates even when installed",
			pkg:       withLatest(Package{name: "rsync"}),
			installed: true,
			wantDNF:   []string{"update", "-y", "rsync"},
			wantNote:  resource.StatusChanged,
		},
		{
			name:     "dry-run on would-act path notes would-change without dnf",
			pkg:      Package{name: "rsync"},
			dryRun:   true,
			wantNote: resource.StatusWouldChange,
		},
		{
			// Converged paths note ok before the dry-run check, like the BSD
			// backends.
			name:     "dry-run on converged path stays ok",
			pkg:      withAbsent(Package{name: "rsync"}),
			dryRun:   true,
			wantNote: resource.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resource.ResetReport()
			resource.SetDryRun(tt.dryRun)

			var calls []dnfInvocation
			runCmd = fakeDNFRunner(tt.installed, &calls)

			if err := applyDNF(&tt.pkg); err != nil {
				t.Fatalf("applyDNF: %v", err)
			}

			var dnfCalls []dnfInvocation
			for _, c := range calls {
				if c.name == "dnf" {
					dnfCalls = append(dnfCalls, c)
				}
			}
			if tt.wantDNF == nil {
				if len(dnfCalls) != 0 {
					t.Errorf("dnf ran %d time(s), want 0; all calls: %v", len(dnfCalls), calls)
				}
				probe := dnfInvocation{name: "rpm", args: []string{"-q", tt.pkg.name}}
				if len(calls) != 1 || calls[0].name != probe.name || !slices.Equal(calls[0].args, probe.args) {
					t.Errorf("expected exactly one rpm -q probe, calls: %v", calls)
				}
			} else {
				if len(dnfCalls) != 1 {
					t.Fatalf("dnf ran %d time(s), want 1; all calls: %v", len(dnfCalls), calls)
				}
				if !slices.Equal(dnfCalls[0].args, tt.wantDNF) {
					t.Errorf("dnf args = %v, want %v", dnfCalls[0].args, tt.wantDNF)
				}
				// Pin probe-before-action ordering: exactly one rpm -q probe
				// precedes the single dnf call, nothing else runs.
				if len(calls) != 2 || calls[0].name != "rpm" {
					t.Errorf("expected exactly one rpm probe before the dnf action, calls: %v", calls)
				}
			}

			assertPkgNote(t, tt.pkg.name, tt.wantNote)
		})
	}
}

// assertPkgNote checks the note recorded for Package[name]: PrintSummary
// lists only non-ok notes, so ok means the id must be absent from the
// summary, changed/would-change mean the id must appear with that status.
func assertPkgNote(t *testing.T, name string, want resource.Status) {
	t.Helper()
	var buf bytes.Buffer
	resource.PrintSummary(&buf)
	summary := buf.String()
	id := "Package[" + name + "]"

	switch want {
	case resource.StatusOK:
		if strings.Contains(summary, id) {
			t.Errorf("expected %s to be noted ok, summary:\n%s", id, summary)
		}
	case resource.StatusChanged, resource.StatusWouldChange:
		wantStr := want.String() + " " + id
		if !strings.Contains(summary, wantStr) {
			t.Errorf("expected %q in summary, got:\n%s", wantStr, summary)
		}
	default:
		t.Fatalf("unexpected want status %v", want)
	}
}

// TestApplyDNFActionStartError pins the dnf-specific wrapper for an action
// that fails to start (e.g. dnf missing), as opposed to a non-zero exit.
func TestApplyDNFActionStartError(t *testing.T) {
	old := runCmd
	defer func() { runCmd = old }()

	runCmd = func(name string, args ...string) (string, string, int, error) {
		if name == "rpm" {
			return "", "", 1, nil // not installed
		}
		return "", "", -1, errors.New("exec: dnf not found")
	}

	resource.ResetReport()
	p := Package{name: "rsync"}
	err := applyDNF(&p)
	if err == nil || !strings.Contains(err.Error(), "failed to execute dnf") {
		t.Errorf("applyDNF should wrap the dnf start failure, got: %v", err)
	}
}

// TestApplyDNFProbeStartError pins that a probe which fails to start (e.g.
// rpm missing) is an error, unlike a non-zero rpm exit (not installed).
func TestApplyDNFProbeStartError(t *testing.T) {
	old := runCmd
	defer func() { runCmd = old }()

	runCmd = func(string, ...string) (string, string, int, error) {
		return "", "", -1, errors.New("exec: rpm not found")
	}

	resource.ResetReport()
	p := Package{name: "rsync"}
	if err := applyDNF(&p); err == nil {
		t.Error("applyDNF should fail when the rpm probe fails to start")
	}
}

// TestApplyDNFActionFailure pins the dnf-specific error message for a
// non-zero dnf exit.
func TestApplyDNFActionFailure(t *testing.T) {
	old := runCmd
	defer func() { runCmd = old }()

	runCmd = func(name string, args ...string) (string, string, int, error) {
		if name == "rpm" {
			return "", "", 1, nil // not installed
		}
		return "some stdout", "some stderr", 3, nil
	}

	resource.ResetReport()
	p := Package{name: "rsync"}
	err := applyDNF(&p)
	if err == nil {
		t.Fatal("applyDNF should fail when dnf exits non-zero")
	}
	if !strings.Contains(err.Error(), "dnf failed with exit code 3") {
		t.Errorf("unexpected error: %v", err)
	}
}
