package api

import (
	"os"
	"runtime"

	"github.com/snonux/gonf/internal/hostfacts"
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

// ProfileOverride returns the currently active CLI -profile override, or ""
// if none is set. Used to re-propagate the override into locally re-exec'd
// elevated apply chunks (see elevatedApplyArgv in apply_chunks.go), since the
// sudo/doas child otherwise starts fresh and DetectFacts() re-derives the
// profile from the host instead of inheriting the parent's override.
func ProfileOverride() string {
	return profileOverride
}

// DetectFacts builds Facts from the running system and any profile override.
// The profile comes from internal/hostfacts ("darwin" on macOS, the
// os-release ID on Linux, "unknown" on the BSDs); the same code runs on the
// controller (task selection) and on the destination (ApplyPlan), so both
// agree on a host.
func DetectFacts() Facts {
	f := Facts{
		GOOS:     runtime.GOOS,
		Hostname: hostname(),
		Profile:  hostfacts.Profile(hostname(), runtime.GOOS),
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
