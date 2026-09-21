package user

import (
	"fmt"
	"path"
	"sort"
	"strings"
)

// Backend reconciles DesiredUser values on one platform. Every backend
// declares, through Capabilities, which parts of the shared DesiredUser
// contract it honours and which it refuses, and Ensure enforces exactly that
// declaration through the shared converge flow. No backend may refuse a
// request ad hoc: every refusal, including the NetBSD membership-union group
// limit, reads the declared Capabilities, and the conformance test
// (capabilities_test.go) drives every backend over every DesiredUser field
// and fails when behaviour and declaration disagree.
type Backend interface {
	// Ensure converges want without destructive account operations and
	// reports every mutation of the account under id, the caller's resource
	// ID (resource.FormatID("User", want.Name) for the User resource).
	Ensure(id string, want DesiredUser) error
	// Capabilities returns the backend's declared capability set.
	Capabilities() Capabilities
}

// Capabilities declares how one backend treats the optional DesiredUser
// fields. Fields not listed here (PrimaryGroup, SupplementaryGroups, Home,
// CreateHome, Shell, ManageHome) are honoured by every backend.
//
// A refusal is enforced at one of two points, and both happen before any
// mutating command:
//   - request refusals (LoginClass, MaxSupplementaryGroups,
//     CreateHomeNeedsSafeHome) right after DesiredUser.Validate, before the
//     account probe, because they do not depend on the destination state;
//   - creation refusals (SystemAccount) after the account probe and only for
//     a missing account, because the field is creation-only and an existing
//     account converges without it.
//
// The target GOOS is unknown when a plan is recorded, so a record-time check
// can only reject what every backend refuses (see ValidateForAnyBackend).
type Capabilities struct {
	// Platform names the platform in refusal messages ("Linux", "BSD" for
	// the shared OpenBSD/NetBSD backend, "FreeBSD").
	Platform string
	// LoginClass reports that DesiredUser.LoginClass is passed to account
	// creation. Without it a non-empty login class is a request refusal,
	// even for an existing account.
	LoginClass bool
	// SystemAccount reports that DesiredUser.System creates a system account.
	// Without it System is a creation refusal: refused when the account is
	// missing, ignored (creation-only) when it exists.
	SystemAccount bool
	// MaxSupplementaryGroups is the largest accepted number of distinct
	// supplementary groups (DesiredUser.Supplementary); 0 means no limit.
	// More is a request refusal. It counts supplementary groups only, not
	// the primary group; whether the platform limit includes the primary
	// group is unverified without a native host (task y42).
	MaxSupplementaryGroups int
	// CreateHomeNeedsSafeHome makes CreateHome with an explicit Home that is
	// relative or the root directory a request refusal, for tools that would
	// create and chown such a directory.
	CreateHomeNeedsSafeHome bool
}

// checkRequest enforces the request refusals: those that do not depend on
// whether the account exists. It runs before any command.
func (c Capabilities) checkRequest(want DesiredUser) error {
	if reason := c.requestRefusal(want); reason != "" {
		return fmt.Errorf("user %q: %s", want.Name, reason)
	}
	return nil
}

// requestRefusal returns why the request refusals reject want, without the
// account prefix, or "" when they accept it.
func (c Capabilities) requestRefusal(want DesiredUser) string {
	if want.LoginClass != "" && !c.LoginClass {
		return "login classes are not supported on " + c.Platform
	}
	if n := len(want.Supplementary()); c.exceedsGroupLimit(n) {
		return fmt.Sprintf("at most %d supplementary groups are supported on %s", c.MaxSupplementaryGroups, c.Platform)
	}
	if c.CreateHomeNeedsSafeHome {
		return unsafeCreatedHome(want)
	}
	return ""
}

// exceedsGroupLimit reports whether n supplementary groups exceed the
// declared MaxSupplementaryGroups. Every group-count check, including the
// NetBSD membership union, goes through it.
func (c Capabilities) exceedsGroupLimit(n int) bool {
	return c.MaxSupplementaryGroups > 0 && n > c.MaxSupplementaryGroups
}

// checkCreate enforces the creation refusals. converge calls it only for a
// missing account, after the probe and before the first mutation.
func (c Capabilities) checkCreate(want DesiredUser) error {
	if want.System && !c.SystemAccount {
		return fmt.Errorf("user %q: system accounts are not supported on %s", want.Name, c.Platform)
	}
	return nil
}

// unsafeCreatedHome returns why creating want's home is refused (a relative
// or root home directory), or "" when it is allowed.
func unsafeCreatedHome(want DesiredUser) string {
	switch {
	case !want.CreateHome || want.Home == "":
		return ""
	case !path.IsAbs(want.Home):
		return "home must be absolute when CreateHome is set"
	case path.Clean(want.Home) == "/":
		return "home must not be the root directory when CreateHome is set"
	}
	return ""
}

// ensure is the one Ensure implementation behind every backend: validate the
// request, enforce the declared request refusals, then converge p (which
// enforces the creation refusals).
func ensure(p accountPlatform, want DesiredUser) error {
	if err := want.Validate(); err != nil {
		return err
	}
	if err := p.Capabilities().checkRequest(want); err != nil {
		return err
	}
	return converge(p, want)
}

// ValidateForAnyBackend is the record-time check of the plan wire. The target
// platform is not known when a plan is recorded, so it rejects only requests
// that every backend refuses before running a command: a malformed request
// (DesiredUser.Validate) or one that each backend's request refusals reject.
// Anything some platform accepts records unchanged and is checked again on
// the destination.
func ValidateForAnyBackend(want DesiredUser) error {
	if err := want.Validate(); err != nil {
		return err
	}
	refusals := make([]string, 0, len(backendConstructors))
	for _, goos := range SupportedGOOS() {
		backend, _ := ForGOOS(goos, nil)
		reason := backend.Capabilities().requestRefusal(want)
		if reason == "" {
			return nil
		}
		refusals = append(refusals, goos+": "+reason)
	}
	return fmt.Errorf("user %q: no supported platform accepts this request (%s)", want.Name, strings.Join(refusals, "; "))
}

var (
	_ Backend = Linux{}
	_ Backend = OpenBSD{}
	_ Backend = NetBSD{}
	_ Backend = FreeBSD{}
)

// backendConstructors maps each GOOS gonf manages accounts on to its backend.
// Every Linux distribution uses the shadow-utils backend (see Linux).
var backendConstructors = map[string]func(Runner) Backend{
	"linux":   func(r Runner) Backend { return NewLinux(r) },
	"openbsd": func(r Runner) Backend { return NewOpenBSD(r) },
	"netbsd":  func(r Runner) Backend { return NewNetBSD(r) },
	"freebsd": func(r Runner) Backend { return NewFreeBSD(r) },
}

// ForGOOS returns the backend for goos with runner (nil selects the normal
// bounded command runner), and false when gonf has no user backend there.
func ForGOOS(goos string, runner Runner) (Backend, bool) {
	newBackend, ok := backendConstructors[goos]
	if !ok {
		return nil, false
	}
	return newBackend(runner), true
}

// SupportedGOOS returns the sorted GOOS values ForGOOS accepts.
func SupportedGOOS() []string {
	goos := make([]string, 0, len(backendConstructors))
	for name := range backendConstructors {
		goos = append(goos, name)
	}
	sort.Strings(goos)
	return goos
}
