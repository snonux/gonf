// Package hostfacts derives the host "profile" fact (fedora, rocky, darwin,
// ...) that task When guards, when_begin predicates and templates'
// {{.Gonf.Profile}} match. It is the one implementation behind
// api.DetectFacts (the controller for task selection, and the destination
// at apply), resource/file's template facts and internal/testapply, so the
// three can no longer drift apart.
package hostfacts

import (
	"bufio"
	"os"
	"strings"
)

// Unknown is the profile of a host gonf cannot classify.
const Unknown = "unknown"

// osReleasePath is the os-release file read for the Linux distribution ID; a
// variable so tests can point it at a fixture.
var osReleasePath = "/etc/os-release"

// Profile returns the profile of a host named hostname running goos:
//   - "darwin" on macOS, which has no os-release;
//   - "rocky" when the hostname contains "rocky" (case-insensitively);
//   - otherwise the os-release ID, with the RHEL family (rocky, centos,
//     rhel, almalinux) folded into "rocky";
//   - Unknown when there is no readable os-release ID (e.g. the BSDs).
func Profile(hostname, goos string) string {
	if goos == "darwin" {
		return "darwin"
	}
	if strings.Contains(strings.ToLower(hostname), "rocky") {
		return "rocky"
	}
	switch id := osReleaseID(); id {
	case "rocky", "centos", "rhel", "almalinux":
		return "rocky"
	case "":
		return Unknown
	default:
		return id
	}
}

// osReleaseID returns the unquoted ID= value of the os-release file, or ""
// when it is unreadable or has none.
func osReleaseID() string {
	f, err := os.Open(osReleasePath)
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), "ID="); ok {
			return strings.Trim(v, `"`)
		}
	}
	return ""
}
