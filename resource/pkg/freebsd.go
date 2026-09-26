package pkg

// freebsdBackend manages packages with FreeBSD pkg(8).
type freebsdBackend struct{ checkedExec }

var _ backend = freebsdBackend{}

// installed probes with pkg info -e, which exits 0 only when installed.
func (freebsdBackend) installed(run runner, name string) (bool, error) {
	return probeExitZero(run, "pkg info -e "+name, "pkg", "info", "-e", name)
}

func (freebsdBackend) installCmd(name string) command { return freebsdCmd("install", "-y", name) }

// upgradeCmd runs pkg upgrade for an installed package (pkg upgrade of an
// up-to-date package is a no-op that is still reported as a change). A
// missing package is installed with pkg install instead: pkg-upgrade(8)
// "will not install new packages".
func (b freebsdBackend) upgradeCmd(name string, installed bool) command {
	if !installed {
		return b.installCmd(name)
	}
	return freebsdCmd("upgrade", "-y", name)
}

func (freebsdBackend) removeCmd(name string) command { return freebsdCmd("remove", "-y", name) }

func freebsdCmd(args ...string) command {
	return command{bin: "pkg", label: "pkg", args: args}
}
