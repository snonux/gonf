package service

import (
	"github.com/snonux/gonf/internal/exec"
	"github.com/snonux/gonf/internal/testseam"
)

// runCmd is the runner the BSD backends (rcctl and service(8)) are
// constructed with when selectBackend picks them: the real runner, or the
// fake a test in this module installed with
// internal/testseam.FakeServiceRunner. In-package tests may instead give a
// backend its own runner. The systemctl backend does not use it: it routes
// through the shared runner in resource/systemd, which FakeServiceRunner
// fakes alongside this one.
func runCmd(name string, args ...string) (string, string, int, error) {
	if fake := testseam.ServiceRunner(); fake != nil {
		return fake(name, args...)
	}
	return exec.Run(name, args...)
}

// detectSvcManager names the host's service manager; selectBackend maps the
// name to a backend. A test in this module can force a name with
// internal/testseam.FakeServiceManager (mirroring resource/pkg's
// detectPkgManager), so the fitness test drives the BSD/rcctl backends on
// any single host instead of only ever reaching whichever backend
// runtime.GOOS happens to select. In-package tests may instead hand a
// backend to applyWith directly.
func detectSvcManager() (string, error) {
	if fake := testseam.ServiceManager(); fake != nil {
		return fake()
	}
	return detectServiceManager()
}
