package pkg

const (
	netbsdPkgin   = "/usr/pkg/bin/pkgin"
	netbsdPkgInfo = "/usr/sbin/pkg_info"
)

// netbsdBackend manages packages with pkgin, probing with pkg_info. Both are
// invoked by absolute path so the backend does not depend on /usr/pkg/bin
// being on the applying user's PATH.
type netbsdBackend struct{ checkedExec }

var _ backend = netbsdBackend{}

// installed probes with pkg_info -e, which exits 0 only when installed.
func (netbsdBackend) installed(run runner, name string) (bool, error) {
	return probeExitZero(run, "pkg_info -e "+name, netbsdPkgInfo, "-e", name)
}

func (netbsdBackend) installCmd(name string) command { return pkginCmd("-y", "install", name) }

// upgradeCmd reuses pkgin install, which upgrades when a newer version is
// available (and installs when the package is missing).
func (netbsdBackend) upgradeCmd(name string, _ bool) command {
	return pkginCmd("-y", "install", name)
}

func (netbsdBackend) removeCmd(name string) command { return pkginCmd("-y", "remove", name) }

func pkginCmd(args ...string) command {
	return command{bin: netbsdPkgin, label: "pkgin", args: args}
}
