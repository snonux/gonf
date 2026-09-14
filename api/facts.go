package api

import (
	"bufio"
	"os"
	"runtime"
	"strings"
)

// Facts describes the host used to evaluate task When predicates.
type Facts struct {
	Profile  string // fedora | rocky | ...
	GOOS     string
	Hostname string
}

var profileOverride string

// SetProfileOverride forces Facts.Profile (e.g. from CLI -profile).
// Pass empty to clear. api.ResetForTest resets it for tests.
func SetProfileOverride(profile string) {
	profileOverride = profile
}

// DetectFacts builds Facts from the running system and any profile override.
func DetectFacts() Facts {
	f := Facts{
		GOOS:     runtime.GOOS,
		Hostname: hostname(),
		Profile:  detectProfile(hostname()),
	}
	if profileOverride != "" {
		f.Profile = profileOverride
	}
	return f
}

func hostname() string {
	h, err := os.Hostname()
	if err != nil {
		return ""
	}
	return h
}

func detectProfile(host string) string {
	if strings.Contains(strings.ToLower(host), "rocky") {
		return "rocky"
	}
	id := osReleaseID()
	switch id {
	case "fedora":
		return "fedora"
	case "rocky", "centos", "rhel", "almalinux":
		return "rocky"
	default:
		if id != "" {
			return id
		}
		return "unknown"
	}
}

func osReleaseID() string {
	f, err := os.Open("/etc/os-release")
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "ID=") {
			v := strings.TrimPrefix(line, "ID=")
			return strings.Trim(v, `"`)
		}
	}
	return ""
}

// ProfileIs returns a When predicate that matches Facts.Profile.
func ProfileIs(profiles ...string) func(Facts) bool {
	set := make(map[string]struct{}, len(profiles))
	for _, p := range profiles {
		set[p] = struct{}{}
	}
	return func(f Facts) bool {
		_, ok := set[f.Profile]
		return ok
	}
}
