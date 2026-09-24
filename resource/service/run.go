package service

import "github.com/snonux/gonf/internal/exec"

// runCmd is the real runner the BSD backends (rcctl and service(8)) use
// when a Service was built without an injected runners.ServiceRunners
// override (Service.run): it always reaches the host directly. The systemd
// backend does not use it: it routes through resource/systemd's Client
// (Service.sysClient, injected separately from a runners.SystemdRunners).
func runCmd(name string, args ...string) (string, string, int, error) {
	return exec.Run(name, args...)
}
