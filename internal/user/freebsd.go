package user

import (
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// freeBSDNoUserExit is sysexits.h's EX_NOUSER. pw(8) uses it when usershow
// cannot find the requested account.
const freeBSDNoUserExit = 67

// freeBSDDataErrExit is sysexits.h's EX_DATAERR. pw(8) groupshow exits with
// it, printing "unknown group", when the group does not exist; the message
// tells that apart from every other data error.
const freeBSDDataErrExit = 65

// FreeBSD reconciles DesiredUser values with FreeBSD's pw(8) utility. It
// creates only missing groups and users, and adds only missing supplementary
// memberships. It never deletes an account or group, removes a membership, or
// changes an existing account's primary group, shell, or login class. An
// existing account's home field changes only when DesiredUser.ManageHome opts
// in, and then only via pw usermod -d without -m.
type FreeBSD struct {
	commands
}

// NewFreeBSD constructs a FreeBSD backend with runner. A nil runner uses the
// normal bounded command runner.
func NewFreeBSD(runner Runner) FreeBSD {
	return FreeBSD{commands{run: defaultRunner(runner)}}
}

// Ensure converges want without destructive account operations, reporting
// account mutations under id. b is a value copy, so setting userID here
// never leaks into another call.
func (b FreeBSD) Ensure(id string, want DesiredUser) error {
	b.userID = id
	return ensure(b, want)
}

// Capabilities declares the pw(8) contract: -L sets a login class, there is
// no system-account flag, the supplementary-group list has no gonf-side
// limit, and pw useradd -m must not be pointed at a relative or root home.
func (FreeBSD) Capabilities() Capabilities {
	return Capabilities{Platform: "FreeBSD", LoginClass: true, CreateHomeNeedsSafeHome: true}
}

// lookupUser reads the account's pw usershow record (master.passwd layout).
func (b FreeBSD) lookupUser(name string) (string, bool, error) {
	return b.pwShow("usershow", name)
}

// ensureExistingUser adds missing memberships and then, only when opted in,
// converges the passwd home field. record is the pw usershow line read
// before any mutation; a membership update never alters the home field.
func (b FreeBSD) ensureExistingUser(record string, want DesiredUser) error {
	if err := b.addMissingMemberships(record, want); err != nil {
		return err
	}
	return b.ensureHomeField(record, want)
}

// ensureHomeField rewrites only the passwd home field. pw usermod -d without
// -m neither creates, moves, nor chowns the directory, and leaves the
// password, lock state, shell, and login class untouched.
func (b FreeBSD) ensureHomeField(record string, want DesiredUser) error {
	if !want.ManageHome {
		return nil
	}
	current, err := passwdHome(record, want.Name, freeBSDHomeField)
	if err != nil || current == want.Home {
		return err
	}
	return b.mutateUser("pw", "usermod", "-n", want.Name, "-d", want.Home)
}

// addMissingMemberships rewrites the secondary-group list only when a
// requested group is missing from it.
//
// pw usermod -G replaces every secondary membership. groupshow -a is the
// complete local group database that pw will rewrite, unlike id -Gn whose
// output is bounded by the platform's supplementary-group limit. It is read
// before any mutation so the replacement list retains every local secondary
// membership while excluding the real primary group as pw(8) requires.
func (b FreeBSD) addMissingMemberships(record string, want DesiredUser) error {
	desired := want.Supplementary()
	if len(desired) == 0 {
		return nil
	}
	primary, current, err := b.secondaryGroups(want.Name, record)
	if err != nil {
		return err
	}
	notPrimary := slices.DeleteFunc(slices.Clone(desired), func(group string) bool { return group == primary })
	if err := ensureEach(notPrimary, b.ensureGroup); err != nil {
		return err
	}
	groups := addSecondaryGroups(current, desired, primary)
	if slices.Equal(groups, current) {
		return nil
	}
	return b.mutateUser("pw", "usermod", "-n", want.Name, "-G", strings.Join(groups, ","))
}

// ensureMissingUser creates every missing creation group, then the account.
// converge has already refused a system account (see Capabilities).
func (b FreeBSD) ensureMissingUser(want DesiredUser) error {
	if err := ensureEach(creationGroups(want), b.ensureGroup); err != nil {
		return err
	}
	return b.addUser(want)
}

func (b FreeBSD) ensureGroup(group string) error {
	return b.ensureGroupWith(b.groupExists, group, "pw", "groupadd", "-n", group)
}

func (b FreeBSD) addUser(want DesiredUser) error {
	args := make([]string, 0, 15)
	args = append(args, "useradd", "-n", want.Name)
	if want.CreateHome {
		args = append(args, "-m")
	}
	args = append(args, "-g", creationPrimaryGroup(want))
	if groups := creationSupplementaryGroups(want); len(groups) > 0 {
		args = append(args, "-G", strings.Join(groups, ","))
	}
	if want.Home != "" {
		args = append(args, "-d", want.Home)
	}
	if want.Shell != "" {
		args = append(args, "-s", want.Shell)
	}
	if want.LoginClass != "" {
		args = append(args, "-L", want.LoginClass)
	}
	return b.mutateUser("pw", args...)
}

// pwShow runs pw <show> -n name (usershow or groupshow) and returns the
// record it printed. EX_NOUSER, or groupshow's EX_DATAERR "unknown group",
// means the entry does not exist.
func (b FreeBSD) pwShow(show, name string) (string, bool, error) {
	stdout, stderr, code, err := b.run("pw", show, "-n", name)
	if err != nil {
		return "", false, fmt.Errorf("pw %s -n %s: %w", show, name, err)
	}
	switch code {
	case 0:
		return stdout, true, nil
	case freeBSDNoUserExit:
		return "", false, nil
	case freeBSDDataErrExit:
		if show == "groupshow" && strings.Contains(stderr, "unknown group") {
			return "", false, nil
		}
		return "", false, commandError("pw", []string{show, "-n", name}, code, stdout, stderr)
	default:
		return "", false, commandError("pw", []string{show, "-n", name}, code, stdout, stderr)
	}
}

func (b FreeBSD) groupExists(group string) (bool, error) {
	_, exists, err := b.pwShow("groupshow", group)
	return exists, err
}

// secondaryGroups returns the name of the account's primary group and the
// sorted groups (other than the primary) whose member list names it, read
// from pw groupshow -a.
func (b FreeBSD) secondaryGroups(name, record string) (string, []string, error) {
	gid, err := primaryGID(name, record)
	if err != nil {
		return "", nil, err
	}
	stdout, err := b.probe("pw", "groupshow", "-a")
	if err != nil {
		return "", nil, err
	}
	var primary string
	groups := make(map[string]struct{})
	for _, line := range strings.Split(strings.TrimSpace(stdout), "\n") {
		if line == "" {
			continue
		}
		group, groupID, members, err := parseFreeBSDGroup(line)
		if err != nil {
			return "", nil, err
		}
		if groupID == gid {
			primary = group
			continue
		}
		if slices.Contains(members, name) {
			groups[group] = struct{}{}
		}
	}
	if primary == "" {
		return "", nil, fmt.Errorf("pw groupshow -a did not contain primary gid %d for user %q", gid, name)
	}
	return primary, sortedGroups(groups), nil
}

// primaryGID parses the gid column of name's pw usershow record.
func primaryGID(name, record string) (uint64, error) {
	fields := strings.Split(strings.TrimSpace(record), ":")
	if len(fields) < 4 || fields[0] != name {
		return 0, fmt.Errorf("pw usershow -n %s returned malformed passwd entry", name)
	}
	gid, err := strconv.ParseUint(fields[3], 10, 32)
	if err != nil {
		return 0, fmt.Errorf("pw usershow -n %s returned invalid primary gid %q: %w", name, fields[3], err)
	}
	return gid, nil
}

// parseFreeBSDGroup splits one pw groupshow -a line (name:pw:gid:members).
// The name is validated so a surprising entry cannot inject a comma or
// whitespace into the -G value built from it.
func parseFreeBSDGroup(line string) (string, uint64, []string, error) {
	fields := strings.SplitN(line, ":", 4)
	if len(fields) != 4 {
		return "", 0, nil, fmt.Errorf("pw groupshow -a returned malformed group entry %q", line)
	}
	if err := validateName("group returned by pw groupshow", fields[0]); err != nil {
		return "", 0, nil, err
	}
	gid, err := strconv.ParseUint(fields[2], 10, 32)
	if err != nil {
		return "", 0, nil, fmt.Errorf("pw groupshow -a returned invalid gid %q: %w", fields[2], err)
	}
	return fields[0], gid, strings.Split(fields[3], ","), nil
}

func creationPrimaryGroup(want DesiredUser) string {
	if want.PrimaryGroup != "" {
		return want.PrimaryGroup
	}
	return want.Name
}

func creationGroups(want DesiredUser) []string {
	groups := want.Groups()
	if want.PrimaryGroup != "" {
		return groups
	}
	groups = append(groups, want.Name)
	sort.Strings(groups)
	return slices.Compact(groups)
}

func creationSupplementaryGroups(want DesiredUser) []string {
	groups := want.Supplementary()
	return slices.DeleteFunc(groups, func(group string) bool {
		return group == creationPrimaryGroup(want)
	})
}

func addSecondaryGroups(current, desired []string, primary string) []string {
	groups := make(map[string]struct{}, len(current)+len(desired))
	for _, group := range current {
		if group != primary {
			groups[group] = struct{}{}
		}
	}
	for _, group := range desired {
		if group != primary {
			groups[group] = struct{}{}
		}
	}
	return sortedGroups(groups)
}

func sortedGroups(groups map[string]struct{}) []string {
	if len(groups) == 0 {
		return nil
	}
	out := make([]string, 0, len(groups))
	for group := range groups {
		out = append(out, group)
	}
	sort.Strings(out)
	return out
}
