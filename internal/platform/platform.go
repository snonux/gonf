// Package platform is the single source of truth for the operating systems
// (GOOS values) gonf manages, for the code that goes through it: the
// inventory's WithGOOS, WithGOARCH and WithPlatform host options, the WhenOS
// task guards (api/task_goos.go) and push's "uname -s" probe
// (internal/remote/sync_probe.go). Adding an OS there is one edit to
// supported and unameNames below (task cb); before, the list was
// hand-written at each site and could drift apart. Other per-OS tables
// (package, service and user backends) are not covered.
package platform

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// The GOOS names gonf manages, spelled as Go's runtime.GOOS spells them.
const (
	Linux   = "linux"
	Darwin  = "darwin"
	FreeBSD = "freebsd"
	OpenBSD = "openbsd"
	NetBSD  = "netbsd"
)

// supported lists the managed GOOS names in the order error messages and
// Supported return them.
var supported = []string{Linux, Darwin, FreeBSD, OpenBSD, NetBSD}

// bsd lists the BSDs among supported (WhenBSD's set).
var bsd = []string{FreeBSD, OpenBSD, NetBSD}

// unameNames maps the kernel name "uname -s" prints on each managed OS to
// its GOOS. It must cover supported exactly (pinned by the tests).
var unameNames = []struct{ uname, goos string }{
	{"Linux", Linux},
	{"Darwin", Darwin},
	{"FreeBSD", FreeBSD},
	{"OpenBSD", OpenBSD},
	{"NetBSD", NetBSD},
}

// ErrEmpty is returned by Check for an empty GOOS.
var ErrEmpty = errors.New("empty GOOS")

// Supported returns a copy of the managed GOOS names; the caller may modify it.
func Supported() []string { return slices.Clone(supported) }

// BSD returns a copy of the managed BSD GOOS names; the caller may modify it.
func BSD() []string { return slices.Clone(bsd) }

// IsSupported reports whether goos is a managed GOOS. The match is exact:
// GOOS names are lower case, so "Linux" is not supported.
func IsSupported(goos string) bool { return slices.Contains(supported, goos) }

// List returns the managed GOOS names joined for an error message, e.g.
// "linux, darwin, freebsd, openbsd, netbsd".
func List() string { return strings.Join(supported, ", ") }

// Check returns nil for a managed GOOS and otherwise an error naming the
// managed ones: ErrEmpty (wrapped) for "", else an "unsupported GOOS" error
// that suggests the lower-case spelling when only the case is wrong
// ("Linux"). Callers prefix it with their own context (e.g. "WithGOOS: ").
func Check(goos string) error {
	if goos == "" {
		return fmt.Errorf("%w (want one of %s)", ErrEmpty, List())
	}
	if lower := strings.ToLower(goos); lower != goos && IsSupported(lower) {
		return fmt.Errorf("unsupported GOOS %q (GOOS names are lower case: did you mean %q?)", goos, lower)
	}
	if !IsSupported(goos) {
		return fmt.Errorf("unsupported GOOS %q (want one of %s)", goos, List())
	}
	return nil
}

// FromUname returns the GOOS of a "uname -s" kernel name (e.g. "FreeBSD" ->
// "freebsd") and whether it names a managed OS. Surrounding space is
// ignored and the name is matched case-insensitively, so a differently
// cased uname still resolves.
func FromUname(sys string) (string, bool) {
	sys = strings.TrimSpace(sys)
	for _, n := range unameNames {
		if strings.EqualFold(sys, n.uname) {
			return n.goos, true
		}
	}
	return "", false
}

// CheckGOARCHCase returns an error, suggesting the lower-case spelling, when
// goarch is not lower case ("AMD64"). GOARCH names (amd64, arm64, 386, ...)
// are always lower case; the value itself is not checked against a list,
// since the Go toolchain reports an unknown one when push cross-compiles.
// An empty goarch passes: callers decide whether empty is allowed.
func CheckGOARCHCase(goarch string) error {
	if lower := strings.ToLower(goarch); lower != goarch {
		return fmt.Errorf("GOARCH %q is not lower case: did you mean %q?", goarch, lower)
	}
	return nil
}
