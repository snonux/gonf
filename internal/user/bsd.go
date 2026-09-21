package user

import (
	"fmt"
	"slices"
	"strings"

	gonfexec "github.com/snonux/gonf/internal/exec"
	"github.com/snonux/gonf/resource"
)

// maxBSDSupplementaryGroups is the portable useradd/usermod -G limit. NetBSD
// documents a maximum of 16 groups; rejecting a larger desired set prevents a
// platform utility from silently truncating it. On NetBSD the limit also
// applies to the full union gonf passes to usermod -G for an existing account
// (see membershipUnion).
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
	run Runner
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
	run Runner
}

// bsd is the shared OpenBSD/NetBSD implementation. membership is the only
// behavioural difference between the two platforms.
type bsd struct {
	run        Runner
	membership membershipMode
}

// NewOpenBSD constructs an OpenBSD backend with runner. A nil runner uses the
// normal bounded command runner.
func NewOpenBSD(runner Runner) OpenBSD {
	return OpenBSD{run: defaultRunner(runner)}
}

// NewNetBSD constructs a NetBSD backend with runner. A nil runner uses the
// normal bounded command runner.
func NewNetBSD(runner Runner) NetBSD {
	return NetBSD{run: defaultRunner(runner)}
}

// Ensure converges want without destructive account operations.
func (b OpenBSD) Ensure(want DesiredUser) error {
	return bsd{run: b.run, membership: appendMissing}.Ensure(want)
}

// Ensure converges want without destructive account operations.
func (b NetBSD) Ensure(want DesiredUser) error {
	return bsd{run: b.run, membership: membershipUnion}.Ensure(want)
}

// EnsureOpenBSD reconciles want with the default bounded command runner.
func EnsureOpenBSD(want DesiredUser) error {
	return NewOpenBSD(nil).Ensure(want)
}

// EnsureNetBSD reconciles want with the default bounded command runner.
func EnsureNetBSD(want DesiredUser) error {
	return NewNetBSD(nil).Ensure(want)
}

func defaultRunner(runner Runner) Runner {
	if runner == nil {
		return gonfexec.Run
	}
	return runner
}

func (b bsd) Ensure(want DesiredUser) error {
	if err := want.Validate(); err != nil {
		return err
	}
	if len(want.Supplementary()) > maxBSDSupplementaryGroups {
		return fmt.Errorf("user %q: at most %d supplementary groups are supported on BSD", want.Name, maxBSDSupplementaryGroups)
	}
	record, exists, err := getent(b.run, "passwd", want.Name)
	if err != nil {
		return err
	}
	if exists {
		return b.ensureExistingUser(record, want)
	}
	if want.System {
		return fmt.Errorf("user %q: system accounts are not supported on BSD", want.Name)
	}
	return b.ensureMissingUser(want)
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
	for _, group := range want.Supplementary() {
		if err := b.ensureGroup(group); err != nil {
			return err
		}
	}
	if len(groups) > 0 {
		if err := b.runMutation("User["+want.Name+"]", "usermod", "-G", strings.Join(groups, ","), want.Name); err != nil {
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
	return b.runMutation("User["+want.Name+"]", "usermod", "-d", want.Home, want.Name)
}

func (b bsd) ensureMissingUser(want DesiredUser) error {
	for _, group := range want.Groups() {
		if err := b.ensureGroup(group); err != nil {
			return err
		}
	}
	return b.addUser(want)
}

func (b bsd) ensureGroup(group string) error {
	exists, err := b.groupExists(group)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	return b.runMutation("Group["+group+"]", "groupadd", group)
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
	return b.runMutation("User["+want.Name+"]", "useradd", args...)
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
	current, err := b.userGroups(want.Name)
	if err != nil {
		return nil, err
	}
	missing := make([]string, 0, len(desired))
	for _, group := range desired {
		if !current[group] {
			missing = append(missing, group)
		}
	}
	if len(missing) == 0 || b.membership == appendMissing {
		return missing, nil
	}
	return b.unionWithExplicitGroups(want.Name, missing)
}

// unionWithExplicitGroups returns the sorted union of missing and every group
// that lists name as an explicit member in the group database: the set a
// replacing usermod -G must receive to keep existing memberships. The union
// is checked against maxBSDSupplementaryGroups here, before ensureExistingUser
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
	if len(union) > maxBSDSupplementaryGroups {
		return nil, fmt.Errorf("user %q: adding %s while keeping existing memberships needs %d supplementary groups; at most %d are supported on BSD",
			name, strings.Join(missing, ","), len(union), maxBSDSupplementaryGroups)
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
	stdout, stderr, code, err := b.run("getent", "group")
	if err != nil {
		return nil, fmt.Errorf("getent group: %w", err)
	}
	if code != 0 {
		return nil, commandError("getent", []string{"group"}, code, stdout, stderr)
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

func (b bsd) groupExists(group string) (bool, error) {
	_, exists, err := getent(b.run, "group", group)
	return exists, err
}

func (b bsd) userGroups(name string) (map[string]bool, error) {
	stdout, stderr, code, err := b.run("id", "-Gn", name)
	if err != nil {
		return nil, fmt.Errorf("id -Gn %s: %w", name, err)
	}
	if code != 0 {
		return nil, commandError("id", []string{"-Gn", name}, code, stdout, stderr)
	}
	groups := make(map[string]bool)
	for _, group := range strings.Fields(stdout) {
		groups[group] = true
	}
	return groups, nil
}

func (b bsd) runAction(command string, args ...string) error {
	stdout, stderr, code, err := b.run(command, args...)
	if err != nil {
		return fmt.Errorf("%s %s: %w", command, strings.Join(args, " "), err)
	}
	if code != 0 {
		return commandError(command, args, code, stdout, stderr)
	}
	return nil
}

func (b bsd) runMutation(id, command string, args ...string) error {
	return resource.Mutate(id, "run "+command+" "+strings.Join(args, " "), func() error {
		return b.runAction(command, args...)
	})
}
