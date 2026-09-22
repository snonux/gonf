package pkg

import (
	"testing"

	"github.com/snonux/gonf/internal/testseam"
)

// TestRunnerDefaults pins what runCmd, runCmdWithEnv and detectPkgManager
// resolve to without a testseam fake: the real runners (the environment
// reaches the command) and the real detector.
func TestRunnerDefaults(t *testing.T) {
	if out, _, code, err := runCmd("echo", "real"); err != nil || code != 0 || out != "real\n" {
		t.Fatalf("real runCmd = %q, %d, %v", out, code, err)
	}
	out, _, code, err := runCmdWithEnv([]string{"GONF_PKG_TEST=env"}, "sh", "-c", "echo $GONF_PKG_TEST")
	if err != nil || code != 0 || out != "env\n" {
		t.Fatalf("real runCmdWithEnv = %q, %d, %v", out, code, err)
	}
	wantName, wantErr := detectPackageManager()
	if name, err := detectPkgManager(); name != wantName || (err == nil) != (wantErr == nil) {
		t.Fatalf("detectPkgManager = %q, %v; want the real %q, %v", name, err, wantName, wantErr)
	}
	testseam.FakePackageManager(t, func() (string, error) { return "netbsd", nil })
	if name, err := detectPkgManager(); name != "netbsd" || err != nil {
		t.Fatalf("faked detectPkgManager = %q, %v", name, err)
	}
}
