package service

import "testing"

// TestRunCmdReachesRealRunner pins that runCmd, the real BSD-backend runner,
// always reaches the host directly (task 4e2: it no longer consults a
// process-global internal/testseam fake).
func TestRunCmdReachesRealRunner(t *testing.T) {
	if out, _, code, err := runCmd("echo", "real"); err != nil || code != 0 || out != "real\n" {
		t.Fatalf("real runCmd = %q, %d, %v", out, code, err)
	}
}

// TestServiceRunAndDetectManagerDefaults pins Service.run/detectManager's
// resolution: without an injected runners.ServiceRunners they reach the real
// runCmd/detectServiceManager, and with one they return its overrides.
func TestServiceRunAndDetectManagerDefaults(t *testing.T) {
	var real Service
	if got := real.run(); got == nil {
		t.Fatal("real.run() returned nil")
	}
	wantName, wantErr := detectServiceManager()
	if name, err := real.detectManager(); name != wantName || (err == nil) != (wantErr == nil) {
		t.Fatalf("real.detectManager() = %q, %v; want the real %q, %v", name, err, wantName, wantErr)
	}

	fakeRun := func(string, ...string) (string, string, int, error) { return "fake", "", 0, nil }
	injected := Service{svcRun: fakeRun, svcManager: func() (string, error) { return "rcctl", nil }}
	if out, _, _, _ := injected.run()("echo", "real"); out != "fake" {
		t.Fatalf("injected.run() = %q, want fake", out)
	}
	if name, err := injected.detectManager(); name != "rcctl" || err != nil {
		t.Fatalf("injected.detectManager() = %q, %v", name, err)
	}
}

// TestNewServiceWithInjectsRunners pins that newServiceWith wires a Service's
// svcRun/svcManager fields (and sysClient) from the injected
// *runners.ServiceRunners/*runners.SystemdRunners, or leaves them nil/real
// when the argument is nil, mirroring resource/cmd's newCmdWith contract.
func TestNewServiceWithInjectsRunners(t *testing.T) {
	s := newServiceWith(nil, nil, "d", nil)
	if s.svcRun != nil || s.svcManager != nil {
		t.Fatalf("nil runners leaked overrides: %+v", s)
	}
}
