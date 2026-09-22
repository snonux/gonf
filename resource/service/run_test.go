package service

import (
	"testing"

	"github.com/snonux/gonf/internal/testseam"
)

// TestRunnerDefaultsAndFakes pins the resolution runCmd and detectSvcManager
// do on every call: without a testseam fake they reach the real runner and
// detector, with one they return the fake's answer.
func TestRunnerDefaultsAndFakes(t *testing.T) {
	if out, _, code, err := runCmd("echo", "real"); err != nil || code != 0 || out != "real\n" {
		t.Fatalf("real runCmd = %q, %d, %v", out, code, err)
	}
	wantName, wantErr := detectServiceManager()
	if name, err := detectSvcManager(); name != wantName || (err == nil) != (wantErr == nil) {
		t.Fatalf("detectSvcManager = %q, %v; want the real %q, %v", name, err, wantName, wantErr)
	}

	testseam.FakeServiceRunner(t, func(string, ...string) (string, string, int, error) { return "fake", "", 0, nil })
	testseam.FakeServiceManager(t, func() (string, error) { return "rcctl", nil })
	if out, _, _, _ := runCmd("echo", "real"); out != "fake" {
		t.Fatalf("faked runCmd = %q, want fake", out)
	}
	if name, err := detectSvcManager(); name != "rcctl" || err != nil {
		t.Fatalf("faked detectSvcManager = %q, %v", name, err)
	}
}
