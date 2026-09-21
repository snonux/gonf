package pkg

// openbsdBackend manages packages with pkg_add/pkg_delete, probing with
// pkg_info.
type openbsdBackend struct{ checkedExec }

var _ backend = openbsdBackend{}

// installed probes with the pkgspec stem-*, which matches any version of the
// package.
func (openbsdBackend) installed(run runner, name string) (bool, error) {
	return probeExitZero(run, "pkg_info -e "+name+"-*", "pkg_info", "-e", name+"-*")
}

func (openbsdBackend) installCmd(name string) command { return openbsdCmd("pkg_add", name) }

// upgradeCmd uses pkg_add -u for an installed package; a missing package is
// installed with plain pkg_add instead.
func (b openbsdBackend) upgradeCmd(name string, installed bool) command {
	if !installed {
		return b.installCmd(name)
	}
	return openbsdCmd("pkg_add", "-u", name)
}

func (openbsdBackend) removeCmd(name string) command { return openbsdCmd("pkg_delete", name) }

func openbsdCmd(bin string, args ...string) command {
	return command{bin: bin, label: bin, args: args}
}
