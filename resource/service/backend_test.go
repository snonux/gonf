package service

import (
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/snonux/gonf/internal/testseam"
	"github.com/snonux/gonf/resource"
)

// fakeBackend is an in-memory service manager: fixed probe results, optional
// probe/action failures, and a record of every verb the policy performed.
// It runs no command, so tests using it prove the policy in applyWith
// depends only on the backend interface, with no detector or runner global
// patched.
type fakeBackend struct {
	userErr      error // userSupport result (nil = WithUser supported)
	isRunning    bool
	isEnabled    bool
	runningErr   error
	enabledErr   error
	failVerb     verb // do fails for this verb ("" = never)
	probedUnits  []unit
	done         []verb
	describedFor []verb
}

func (f *fakeBackend) userSupport() error { return f.userErr }

func (f *fakeBackend) running(u unit) (bool, error) {
	f.probedUnits = append(f.probedUnits, u)
	return f.isRunning, f.runningErr
}

func (f *fakeBackend) enabled(u unit) (bool, error) {
	f.probedUnits = append(f.probedUnits, u)
	return f.isEnabled, f.enabledErr
}

func (f *fakeBackend) do(_ unit, v verb) error {
	if v == f.failVerb {
		return errors.New("fake " + string(v) + " failed")
	}
	f.done = append(f.done, v)
	return nil
}

func (f *fakeBackend) describe(u unit, v verb) (would, did string) {
	f.describedFor = append(f.describedFor, v)
	return "fake " + string(v) + " " + u.name, "fake " + string(v) + " " + u.name
}

// policyCase is one row of TestApplyWithFakeBackendPolicy: the Service, the
// fake probe results, and the verbs and note the policy must produce.
type policyCase struct {
	name     string
	svc      Service
	running  bool
	enabled  bool
	dryRun   bool
	wantDone []verb
	wantNote resource.Status
}

// policyCases covers action order for present/absent, reload over restart,
// the change gate holding only the restart/reload, and dry-run.
func policyCases() []policyCase {
	gated := withRestartSvc(Service{name: "d"})
	gated.Gated = true // armed, and no watched resource changed

	return []policyCase{
		{name: "present converged", svc: Service{name: "d"}, running: true, enabled: true, wantNote: resource.StatusOK},
		{name: "present enables then starts", svc: Service{name: "d"},
			wantDone: []verb{verbEnable, verbStart}, wantNote: resource.StatusChanged},
		{name: "absent stops then disables", svc: withAbsentSvc(Service{name: "d"}), running: true, enabled: true,
			wantDone: []verb{verbStop, verbDisable}, wantNote: resource.StatusChanged},
		{name: "absent converged", svc: withAbsentSvc(Service{name: "d"}), wantNote: resource.StatusOK},
		{name: "absent ignores restart", svc: withRestartSvc(withAbsentSvc(Service{name: "d"})), running: true,
			wantDone: []verb{verbStop}, wantNote: resource.StatusChanged},
		{name: "reload wins over restart", svc: withReloadSvc(withRestartSvc(Service{name: "d"})), running: true, enabled: true,
			wantDone: []verb{verbReload}, wantNote: resource.StatusChanged},
		{name: "stopped service starts instead of restarting", svc: withRestartSvc(Service{name: "d"}), enabled: true,
			wantDone: []verb{verbStart}, wantNote: resource.StatusChanged},
		{name: "gate holds restart and reports skipped", svc: gated, running: true, enabled: true, wantNote: resource.StatusSkipped},
		{name: "gate never holds convergence", svc: gated, running: true,
			wantDone: []verb{verbEnable}, wantNote: resource.StatusChanged},
		{name: "dry-run performs nothing", svc: Service{name: "d"}, dryRun: true, wantNote: resource.StatusWouldChange},
	}
}

// TestApplyWithFakeBackendPolicy pins the shared service policy against a
// fake backend injected straight into applyWith; dry-run must perform
// nothing yet still describe the would-be actions and note would-change.
func TestApplyWithFakeBackendPolicy(t *testing.T) {
	oldDry := resource.DryRun()
	t.Cleanup(func() { resource.SetDryRun(oldDry) })

	for _, tt := range policyCases() {
		t.Run(tt.name, func(t *testing.T) {
			resource.ResetReport()
			resource.SetDryRun(tt.dryRun)
			fb := &fakeBackend{isRunning: tt.running, isEnabled: tt.enabled}

			if err := tt.svc.applyWith(fb); err != nil {
				t.Fatalf("applyWith: %v", err)
			}
			if !slices.Equal(fb.done, tt.wantDone) {
				t.Errorf("done = %v, want %v", fb.done, tt.wantDone)
			}
			if tt.dryRun && len(fb.describedFor) == 0 {
				t.Error("dry-run did not describe the would-be actions")
			}
			assertStatus(t, "Service[d]", tt.wantNote)
		})
	}
}

// TestApplyWithFakeBackendErrors pins the negative paths: WithUser on a
// backend without a per-user manager is refused before any probe, probe
// errors surface before any action, and a failing action aborts the rest
// without noting the service as changed.
func TestApplyWithFakeBackendErrors(t *testing.T) {
	oldDry := resource.DryRun()
	t.Cleanup(func() { resource.SetDryRun(oldDry) })
	resource.SetDryRun(false)

	tests := []struct {
		name     string
		svc      Service
		fb       *fakeBackend
		wantErr  string
		wantDone []verb
		wantProb int // number of probes that ran
	}{
		{name: "user unsupported uses the backend's reason", svc: withUserSvc(Service{name: "d"}),
			fb: &fakeBackend{userErr: errors.New("no per-user manager here")}, wantErr: "service[d]: no per-user manager here"},
		{name: "running probe error", svc: Service{name: "d"}, fb: &fakeBackend{runningErr: errors.New("probe boom")},
			wantErr: "probe boom", wantProb: 1},
		{name: "enabled probe error", svc: Service{name: "d"}, fb: &fakeBackend{enabledErr: errors.New("enabled boom")},
			wantErr: "enabled boom", wantProb: 2},
		{name: "failing action aborts the rest", svc: Service{name: "d"}, fb: &fakeBackend{failVerb: verbEnable},
			wantErr: "fake enable failed", wantProb: 2},
		{name: "second action failure keeps the first", svc: Service{name: "d"}, fb: &fakeBackend{failVerb: verbStart},
			wantErr: "fake start failed", wantDone: []verb{verbEnable}, wantProb: 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resource.ResetReport()
			err := tt.svc.applyWith(tt.fb)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want substring %q", err, tt.wantErr)
			}
			if !slices.Equal(tt.fb.done, tt.wantDone) {
				t.Errorf("done = %v, want %v", tt.fb.done, tt.wantDone)
			}
			if len(tt.fb.probedUnits) != tt.wantProb {
				t.Errorf("probes = %d, want %d", len(tt.fb.probedUnits), tt.wantProb)
			}
			assertNotNoted(t, "Service[d]")
		})
	}
}

// TestApplyWithUserReachesUserCapableBackend pins that a backend whose
// userSupport returns nil receives user=true on every probe.
func TestApplyWithUserReachesUserCapableBackend(t *testing.T) {
	resource.ResetReport()
	fb := &fakeBackend{isRunning: true, isEnabled: true}
	svc := withUserSvc(Service{name: "d"})
	if err := svc.applyWith(fb); err != nil {
		t.Fatalf("applyWith: %v", err)
	}
	want := []unit{{name: "d", user: true}, {name: "d", user: true}}
	if !slices.Equal(fb.probedUnits, want) {
		t.Errorf("probed %v, want %v", fb.probedUnits, want)
	}
}

// TestSelectBackend pins the name-to-backend table: each detector name
// yields its backend type (BSD backends wired to the package runner, NetBSD
// to /etc/rc.conf.d), and an unknown name or detector error is refused.
func TestSelectBackend(t *testing.T) {
	want := map[string]backend{
		"systemd": systemdBackend{},
		"rcctl":   rcctlBackend{},
		"freebsd": freebsdBackend{},
		"netbsd":  netbsdBackend{},
	}
	if len(backends) != len(want) {
		t.Fatalf("backends table has %d entries, want %d", len(backends), len(want))
	}
	for name, wantB := range want {
		testseam.FakeServiceManager(t, func() (string, error) { return name, nil })
		got, err := selectBackend()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if reflect.TypeOf(got) != reflect.TypeOf(wantB) {
			t.Errorf("%s selected %T, want %T", name, got, wantB)
		}
		assertWired(t, got)
	}

	testseam.FakeServiceManager(t, func() (string, error) { return "launchd", nil })
	if _, err := selectBackend(); err == nil || !strings.Contains(err.Error(), "unsupported service manager") {
		t.Errorf("unknown manager err = %v, want unsupported service manager", err)
	}
	testseam.FakeServiceManager(t, func() (string, error) { return "", errors.New("detect boom") })
	if _, err := selectBackend(); err == nil || !strings.Contains(err.Error(), "detect boom") {
		t.Errorf("detector err = %v, want detect boom", err)
	}
}

// assertWired checks a selected BSD backend got a runner (and NetBSD the
// production override directory), so it cannot nil-panic on first use.
func assertWired(t *testing.T, b backend) {
	t.Helper()
	switch b := b.(type) {
	case rcctlBackend:
		if b.run == nil {
			t.Error("rcctl backend selected without a runner")
		}
	case freebsdBackend:
		if b.run == nil {
			t.Error("freebsd backend selected without a runner")
		}
	case netbsdBackend:
		if b.run == nil || b.rcConfD != "/etc/rc.conf.d" {
			t.Errorf("netbsd backend wired with run=%v rcConfD=%q", b.run != nil, b.rcConfD)
		}
	}
}

// assertStatus checks the note for id after a ResetReport. The summary lists
// only changed/would-change ids, so ok means id is absent and counted as the
// single ok; skipped is checked through the counter line; the change states
// must appear with their label.
func assertStatus(t *testing.T, id string, want resource.Status) {
	t.Helper()
	var buf strings.Builder
	resource.PrintSummary(&buf)
	summary := buf.String()
	switch want {
	case resource.StatusOK:
		if strings.Contains(summary, id) || !strings.Contains(summary, "1 ok, 0 changed, 0 skipped") {
			t.Errorf("expected %s to be noted ok, summary:\n%s", id, summary)
		}
		return
	case resource.StatusSkipped:
		if !strings.Contains(summary, "0 ok, 0 changed, 1 skipped, 0 would-change") {
			t.Errorf("expected %s to be noted skipped, summary:\n%s", id, summary)
		}
		return
	}
	if wantStr := want.String() + " " + id; !strings.Contains(summary, wantStr) {
		t.Errorf("expected %q in summary, got:\n%s", wantStr, summary)
	}
}

// assertNotNoted checks a failed apply recorded no status for id.
func assertNotNoted(t *testing.T, id string) {
	t.Helper()
	var buf strings.Builder
	resource.PrintSummary(&buf)
	if strings.Contains(buf.String(), id) {
		t.Errorf("failed apply was noted:\n%s", buf.String())
	}
}
