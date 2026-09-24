package pkg

import "testing"

// TestRunnerDefaults pins what a Package built without injected runners
// (build) resolves runCmd, runCmdWithEnv and detectPkgManager to: the real
// runners (the environment reaches the command) and the real detector; an
// injected Manager replaces the detector.
func TestRunnerDefaults(t *testing.T) {
	p, err := build("rsync", nil)
	if err != nil {
		t.Fatal(err)
	}
	if out, _, code, err := p.runCmd("echo", "real"); err != nil || code != 0 || out != "real\n" {
		t.Fatalf("real runCmd = %q, %d, %v", out, code, err)
	}
	out, _, code, err := p.runCmdWithEnv([]string{"GONF_PKG_TEST=env"}, "sh", "-c", "echo $GONF_PKG_TEST")
	if err != nil || code != 0 || out != "env\n" {
		t.Fatalf("real runCmdWithEnv = %q, %d, %v", out, code, err)
	}
	wantName, wantErr := detectPackageManager()
	if name, err := p.detectPkgManager(); name != wantName || (err == nil) != (wantErr == nil) {
		t.Fatalf("detectPkgManager = %q, %v; want the real %q, %v", name, err, wantName, wantErr)
	}
	faked := managedBy(func() (string, error) { return "netbsd", nil })
	if name, err := faked.detectPkgManager(); name != "netbsd" || err != nil {
		t.Fatalf("injected detectPkgManager = %q, %v", name, err)
	}
}
