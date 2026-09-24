package options

import (
	"fmt"
	"os"
	"strings"
)

// Root is the owner spec for "the root user and the root group of the
// destination OS": root:root on Linux, root:wheel on OpenBSD, FreeBSD,
// NetBSD and darwin. Use it as Perm(0o755, Root) or WithOwner(Root).
//
// It records the group as the numeric gid 0 rather than a name. Every
// supported destination defines its root group (root on Linux, wheel on the
// BSDs and darwin) as gid 0, and the kernel identifies a group by its gid, so
// "0" means the right group wherever the plan is applied without any name
// lookup or GOOS branch on either side. The controller's own group database
// is never consulted, and the plan schema is unchanged: the file, dir,
// sync-dir and config-set apply paths already accept a numeric group (they
// parse a numeric string before falling back to a name lookup), so a
// destination running an older gonf applies such a plan identically. The
// user stays the name "root", which every supported OS defines; gonf
// resolves owners by name only.
const Root = "root:" + rootGID

// rootGID is the gid of the root group on every supported destination.
const rootGID = "0"

// Perm sets a file's or directory's mode and ownership in one option. It is
// exactly WithMode(mode) plus WithOwner(user) plus WithGroup(group) for the
// owner spec "user:group"; "user" sets only the owner and ":group" only the
// group, like chown(1). Root selects root and the destination's root group.
// A malformed owner (empty, "user:", ":" or more than one colon) or a mode
// WithMode would refuse is recipe misuse reported to the resource; the option
// then sets nothing.
func Perm(mode os.FileMode, owner string) fileDirOption {
	return fileDirOption(func(target any) {
		// Validate both halves before setting anything, so a misused Perm
		// never leaves a half-applied mode or ownership behind.
		usr, group, err := parseOwnerSpec("Perm", owner)
		if err == nil {
			_, err = normalizeMode(mode)
		}
		if err != nil {
			misuse(target, err)
			return
		}
		setMode(target, "Perm", mode)
		setOwnership(target, "Perm", usr, group)
	})
}

// parseOwnerSpec splits a chown-style owner spec into its user and group
// parts. Either part may be empty (but not both), and "user:" is refused
// rather than guessed at: chown(1) reads it as "the user's login group",
// which gonf does not resolve.
func parseOwnerSpec(label, spec string) (usr, group string, err error) {
	usr, group, hasColon := strings.Cut(spec, ":")
	switch {
	case spec == "":
		return "", "", fmt.Errorf("%s owner is empty: want \"user\", \"user:group\" or \":group\"", label)
	case strings.Contains(group, ":"):
		return "", "", fmt.Errorf("%s owner %q has more than one colon: want \"user\", \"user:group\" or \":group\"", label, spec)
	case hasColon && group == "":
		return "", "", fmt.Errorf("%s owner %q has an empty group: want \"user\", \"user:group\" or \":group\"", label, spec)
	}
	return usr, group, nil
}

// setOwnership applies the parsed parts of an owner spec: a non-empty user
// through SetOwner and a non-empty group through SetGroup, so ":group" never
// records an owner and "user" never records a group. A target lacking a
// needed capability gets a misuse report naming label.
func setOwnership(target any, label, usr, group string) {
	if usr != "" {
		requires(target, label, func(r Owner) { r.SetOwner(usr) })
	}
	if group != "" {
		requires(target, label, func(r Grouped) { r.SetGroup(group) })
	}
}

// setMode validates and applies mode through SetMode, the shared body of
// WithMode and Perm; label names the option in a misuse report.
func setMode(target any, label string, mode os.FileMode) {
	requires(target, label, func(r Moded) {
		normalized, err := normalizeMode(mode)
		if err != nil {
			misuse(target, err)
			return
		}
		r.SetMode(normalized)
	})
}
