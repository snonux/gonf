package user

import (
	"fmt"
	"slices"
	"strings"
)

// maxBSDSupplementaryGroups is the portable useradd/usermod -G limit,
// declared as Capabilities.MaxSupplementaryGroups. NetBSD documents a maximum
// of 16 groups; rejecting a larger desired set prevents a platform utility
// from silently truncating it. On NetBSD the limit also applies to the full
// union gonf passes to usermod -G for an existing account (see
// membershipUnion). It counts supplementary groups only: whether NGROUPS_MAX
// also counts the primary group (16 + 1) is unverified without a native BSD
// host (task y42), so the value is kept as it was.
const maxBSDSupplementaryGroups = 16

// membershipMode selects how a BSD backend adds supplementary memberships to
// an account that already exists. The two BSDs share useradd/usermod
// ancestry but document usermod -G differently.
type membershipMode int

const (
	// appendMissing passes only the missing groups to usermod -G. OpenBSD
	// usermod(8) documents -G as appending to the user's secondary groups
	// (and a separate -S option as replacing them), so the missing set alone
	// never drops an existing membership.
	appendMissing membershipMode = iota
	// membershipUnion passes every group that already lists the account as
	// an explicit member plus the missing ones. NetBSD usermod(8) only says
	// -G names the secondary groups the user will be a member of and offers
	// no separate append/set option, so gonf must not rely on append
	// behaviour: the full union keeps every existing membership whether -G
	// replaces or appends. Under the append reading, re-listing a group the
	// user is already in must not duplicate the member entry; that the
	// shared BSD user.c append step skips such groups is an unverified
	// recollection of the source, pending native verification (task y42).
	membershipUnion
)

// OpenBSD reconciles DesiredUser values using OpenBSD's user-management
// utilities. It creates only missing groups and users, and adds only missing
// supplementary memberships. It never deletes an account or group, removes a
// membership, or changes an existing account's primary group, shell, or login
// class. An existing account's home field changes only when
// DesiredUser.ManageHome opts in, and then only via usermod -d without -m.
// Missing memberships are passed alone to usermod -G, which OpenBSD
// documents as appending (see appendMissing).
type OpenBSD struct {
	bsd
}

// NetBSD reconciles DesiredUser values using NetBSD's user-management
// utilities. It creates only missing groups and users, and adds only missing
// supplementary memberships. It never deletes an account or group, removes a
// membership, or changes an existing account's primary group, shell, or login
// class. An existing account's home field changes only when
// DesiredUser.ManageHome opts in, and then only via usermod -d without -m.
// Because NetBSD does not document whether usermod -G appends or replaces,
// missing memberships are added by passing the full union of the existing
// explicit memberships and the missing groups (see membershipUnion).
type NetBSD struct {
	bsd
}

// bsd is the shared OpenBSD/NetBSD implementation, embedded by both exported
// backends. membership is the only behavioural difference between the two
// platforms.
type bsd struct {
	commands
	membership membershipMode
}

// NewOpenBSD constructs an OpenBSD backend with runner. A nil runner uses the
// normal bounded command runner.
func NewOpenBSD(runner Runner) OpenBSD {
	return OpenBSD{bsd{commands: commands{run: defaultRunner(runner)}, membership: appendMissing}}
}

// NewNetBSD constructs a NetBSD backend with runner. A nil runner uses the
// normal bounded command runner.
func NewNetBSD(runner Runner) NetBSD {
	return NetBSD{bsd{commands: commands{run: defaultRunner(runner)}, membership: membershipUnion}}
}

// Ensure converges want without destructive account operations, reporting
// account mutations under id. OpenBSD and NetBSD expose it through
// embedding; b is a value copy, so setting userID never leaks into another
// call.
func (b bsd) Ensure(id string, want DesiredUser) error {
	b.userID = id
	return ensure(b, want)
}

// Capabilities declares the portable OpenBSD/NetBSD useradd contract: -L sets
// a login class (NetBSD only when useradd is built with EXTENSIONS; a tool
// without it fails loudly rather than dropping the class), there is no
// system-account flag, and at most maxBSDSupplementaryGroups supplementary
// groups are accepted. OpenBSD and NetBSD expose it through embedding.
func (bsd) Capabilities() Capabilities {
	return Capabilities{Platform: "BSD", LoginClass: true, MaxSupplementaryGroups: maxBSDSupplementaryGroups}
}

// lookupUser reads the account's getent passwd record.
func (b bsd) lookupUser(name string) (string, bool, error) {
	return b.getent("passwd", name)
}

// ensureExistingUser adds missing memberships and then, only when opted in,
// converges the passwd home field. record is the getent passwd line read
// before any mutation; membership changes never alter the home field.
//
// The usermod -G argument is computed and validated (membership probes,
// NetBSD group enumeration, name checks, group limit) before the first
// groupadd, so a refused membership update mutates nothing rather than
// leaving freshly created groups behind on every failing run.
func (b bsd) ensureExistingUser(record string, want DesiredUser) error {
	groups, err := b.membershipArgument(want)
	if err != nil {
		return err
	}
	if err := ensureEach(want.Supplementary(), b.ensureGroup); err != nil {
		return err
	}
	if len(groups) > 0 {
		if err := b.mutateUser("usermod", "-G", strings.Join(groups, ","), want.Name); err != nil {
			return err
		}
	}
	return b.ensureHomeField(record, want)
}

// ensureHomeField rewrites only the passwd home field. OpenBSD and NetBSD
// usermod -d without -m neither creates, moves, nor chowns the directory, and
// leaves the password, lock state, shell, and login class untouched.
func (b bsd) ensureHomeField(record string, want DesiredUser) error {
	if !want.ManageHome {
		return nil
	}
	current, err := passwdHome(record, want.Name, getentHomeField)
	if err != nil || current == want.Home {
		return err
	}
	return b.mutateUser("usermod", "-d", want.Home, want.Name)
}

// ensureMissingUser creates every missing requested group, then the account.
// converge has already refused a system account (see Capabilities).
func (b bsd) ensureMissingUser(want DesiredUser) error {
	if err := ensureEach(want.Groups(), b.ensureGroup); err != nil {
		return err
	}
	return b.addUser(want)
}

func (b bsd) ensureGroup(group string) error {
	return b.ensureGroupWith(b.getentGroupExists, group, "groupadd", group)
}

func (b bsd) addUser(want DesiredUser) error {
	args := make([]string, 0, 12)
	if want.CreateHome {
		args = append(args, "-m")
	}
	if want.PrimaryGroup != "" {
		args = append(args, "-g", want.PrimaryGroup)
	}
	if groups := want.Supplementary(); len(groups) > 0 {
		args = append(args, "-G", strings.Join(groups, ","))
	}
	if want.Home != "" {
		args = append(args, "-d", want.Home)
	}
	if want.Shell != "" {
		args = append(args, "-s", want.Shell)
	}
	if want.LoginClass != "" {
		// NetBSD documents -L when useradd is built with EXTENSIONS. If a
		// target lacks that optional support, the useradd failure is returned
		// rather than silently dropping the requested login class.
		args = append(args, "-L", want.LoginClass)
	}
	args = append(args, want.Name)
	return b.mutateUser("useradd", args...)
}

// membershipArgument returns the groups to pass to usermod -G, or nil when
// the account already has every desired supplementary group (an empty
// result means the same). It only probes
// and never mutates. id -Gn decides what is missing, so a converged account
// issues no membership command on either platform; a group that does not
// exist yet is simply reported missing. The argument never removes a
// membership: on OpenBSD it is the missing set, on NetBSD the union built by
// unionWithExplicitGroups (see membershipMode).
func (b bsd) membershipArgument(want DesiredUser) ([]string, error) {
	desired := want.Supplementary()
	if len(desired) == 0 {
		return nil, nil
	}
	current, err := b.idGroups("-Gn", want.Name)
	if err != nil {
		return nil, err
	}
	missing := missingGroups(desired, current)
	if len(missing) == 0 || b.membership == appendMissing {
		return missing, nil
	}
	return b.unionWithExplicitGroups(want.Name, missing)
}

// unionWithExplicitGroups returns the sorted union of missing and every group
// that lists name as an explicit member in the group database: the set a
// replacing usermod -G must receive to keep existing memberships. The union
// is checked against the declared Capabilities().MaxSupplementaryGroups
// here (the same limit the request check enforces), before ensureExistingUser
// creates any group or runs usermod, so a refusal mutates nothing and the
// platform tool can neither truncate the list nor fail half-way through.
func (b bsd) unionWithExplicitGroups(name string, missing []string) ([]string, error) {
	groups, err := b.explicitGroups(name)
	if err != nil {
		return nil, err
	}
	for _, group := range missing {
		groups[group] = struct{}{}
	}
	union := sortedGroups(groups)
	if caps := b.Capabilities(); caps.exceedsGroupLimit(len(union)) {
		return nil, fmt.Errorf("user %q: adding %s while keeping existing memberships needs %d supplementary groups; at most %d are supported on %s",
			name, strings.Join(missing, ","), len(union), caps.MaxSupplementaryGroups, caps.Platform)
	}
	return union, nil
}

// explicitGroups enumerates the group database (getent group) and returns
// the groups whose member list names the account. Unlike id -Gn it is not
// bounded by the kernel's group limit and does not report the primary group
// merely because it is the passwd gid, so it matches the member lists that
// usermod rewrites. A primary group that also lists the account explicitly
// is kept, so that listing survives a replacing -G. Group names are validated
// so a surprising entry cannot inject a comma or whitespace into the -G value.
func (b bsd) explicitGroups(name string) (map[string]struct{}, error) {
	stdout, err := b.probe("getent", "group")
	if err != nil {
		return nil, err
	}
	groups := make(map[string]struct{})
	for _, line := range strings.Split(strings.TrimSpace(stdout), "\n") {
		if line == "" {
			continue
		}
		fields := strings.SplitN(line, ":", 4)
		if len(fields) != 4 {
			return nil, fmt.Errorf("getent group returned malformed group entry %q", line)
		}
		if !slices.Contains(strings.Split(fields[3], ","), name) {
			continue
		}
		if err := validateName("group returned by getent group", fields[0]); err != nil {
			return nil, err
		}
		groups[fields[0]] = struct{}{}
	}
	return groups, nil
}
