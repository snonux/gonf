package pkg

import "fmt"

// dnfBackend manages packages on dnf-based Linux (Fedora, RHEL, Rocky, CentOS).
type dnfBackend struct{}

var _ backend = dnfBackend{}

// installed probes with rpm -q, which only consults the local rpm database:
// unlike `dnf list installed` it never triggers a repository metadata
// refresh, so the probe stays cheap. A non-zero exit code means the package
// is not installed; a start failure is an error.
func (dnfBackend) installed(run runner, name string) (bool, error) {
	return probeExitZero(run, "rpm -q "+name, "rpm", "-q", name)
}

func (dnfBackend) installCmd(name string) command { return dnfCmd("install", "-y", name) }

// upgradeCmd runs dnf update for an installed package; a missing package is
// installed with dnf install instead, because dnf update cannot install:
// for a package that is not installed it fails with "No packages marked for
// upgrade." (exit 1).
func (b dnfBackend) upgradeCmd(name string, installed bool) command {
	if !installed {
		return b.installCmd(name)
	}
	return dnfCmd("update", "-y", name)
}

func (dnfBackend) removeCmd(name string) command { return dnfCmd("remove", "-y", name) }

// execute keeps dnf's historical error wording ("failed to execute dnf",
// "dnf failed with exit code N"; pinned by dnf_test.go) instead of the
// runOrErr format the BSD backends share, so operators see unchanged dnf
// errors after the backend refactor.
func (dnfBackend) execute(run runner, c command) error {
	stdout, stderr, exitCode, err := run(c.bin, c.args...)
	if err != nil {
		return fmt.Errorf("failed to execute dnf: %w", err)
	}
	if exitCode != 0 {
		return fmt.Errorf("dnf failed with exit code %d: %s\n%s", exitCode, stdout, stderr)
	}
	return nil
}

// dnfCmd builds a dnf command; its apply log line keeps the historical
// " completed" suffix.
func dnfCmd(args ...string) command {
	return command{bin: "dnf", label: "dnf", args: args, doneSuffix: " completed"}
}
