package service

import (
	"errors"
	"testing"

	"github.com/snonux/gonf/resource"
)

// TestRealBackendsUserSupport pins each real backend's WithUser capability:
// only systemd has a per-user manager; the BSD backends refuse with the
// historical, user-visible wording.
func TestRealBackendsUserSupport(t *testing.T) {
	if err := (systemdBackend{}).userSupport(); err != nil {
		t.Errorf("systemd userSupport = %v, want nil", err)
	}
	for name, b := range map[string]backend{
		"rcctl":   rcctlBackend{},
		"freebsd": freebsdBackend{},
		"netbsd":  netbsdBackend{},
	} {
		err := b.userSupport()
		if !errors.Is(err, errUserNeedsSystemd) {
			t.Errorf("%s userSupport = %v, want errUserNeedsSystemd", name, err)
			continue
		}
		if err.Error() != "WithUser is only supported on systemd" {
			t.Errorf("%s refusal wording = %q", name, err)
		}
	}
}

// TestWithUserRefusedBeforeProbeOnBSDBackends drives the real BSD backends,
// each with its own recording runner: a WithUser service is refused with
// the unchanged "service[NAME]: WithUser is only supported on systemd"
// error before any probe (or anything else) runs, and nothing is noted.
func TestWithUserRefusedBeforeProbeOnBSDBackends(t *testing.T) {
	var calls []svcCall
	record := func(bin string, args ...string) (string, string, int, error) {
		calls = append(calls, svcCall{bin: bin, args: args})
		return "", "", 0, nil
	}
	for name, b := range map[string]backend{
		"rcctl":   rcctlBackend{run: record},
		"freebsd": freebsdBackend{run: record},
		"netbsd":  netbsdBackend{run: record, rcConfD: t.TempDir()},
	} {
		t.Run(name, func(t *testing.T) {
			calls = nil
			resource.ResetReport()
			svc := withUserSvc(Service{name: "uptimed"})
			err := svc.applyWith(b)
			const want = "service[uptimed]: WithUser is only supported on systemd"
			if err == nil || err.Error() != want {
				t.Fatalf("err = %v, want %q", err, want)
			}
			if len(calls) != 0 {
				t.Errorf("commands ran before the refusal: %v", calls)
			}
			assertNotNoted(t, "Service[uptimed]")
		})
	}
}

// TestRealBackendLogLines pins the operator-visible dry-run and apply log
// lines of every real service backend, so wording changes cannot slip
// through silently.
func TestRealBackendLogLines(t *testing.T) {
	sys, user := unit{name: "sshd"}, unit{name: "sshd", user: true}
	tests := []struct {
		name      string
		b         backend
		u         unit
		v         verb
		wantWould string
		wantDid   string
	}{
		{"systemd", systemdBackend{}, sys, verbRestart,
			"dry-run: would run systemctl [restart sshd]", "systemctl [restart sshd]"},
		{"systemd user", systemdBackend{}, user, verbEnable,
			"dry-run: would run systemctl [--user enable sshd]", "systemctl [--user enable sshd]"},
		{"rcctl", rcctlBackend{}, sys, verbStart,
			"dry-run: would run rcctl [start sshd]", "rcctl [start sshd]"},
		{"freebsd", freebsdBackend{}, sys, verbReload,
			"dry-run: would run service [sshd reload]", "service [sshd reload]"},
		{"netbsd service verb", netbsdBackend{}, sys, verbStop,
			"dry-run: would service sshd onestop", "service sshd onestop"},
		{"netbsd enable", netbsdBackend{}, sys, verbEnable,
			"dry-run: would enable sshd", "enable sshd"},
		{"netbsd disable", netbsdBackend{}, sys, verbDisable,
			"dry-run: would disable sshd", "disable sshd"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			would, did := actionLogLines(tt.b, tt.u, tt.v)
			if would != tt.wantWould {
				t.Errorf("dry-run line = %q, want %q", would, tt.wantWould)
			}
			if did != tt.wantDid {
				t.Errorf("apply line = %q, want %q", did, tt.wantDid)
			}
		})
	}
}
