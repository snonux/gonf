// Package platform is the single source of truth for the operating systems
// (GOOS values) gonf manages. Every place that accepts or derives a GOOS
// checks it here: the WhenOS task guards (api/task_goos.go), the inventory's
// WithGOOS and WithPlatform host options, and push's "uname -s" probe
// (internal/remote/sync_probe.go). Adding an OS is one edit to supported
// below (task cb); before, the list was hand-written at all three sites and
// could drift apart.
//
// Resource backends (package managers, service managers, user backends)
// keep their own per-OS tables: those map an OS to an implementation, not
// to a yes/no, and an OS may be supported by push before every backend has
// learned it.
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
