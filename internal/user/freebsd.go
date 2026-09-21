package user

import (
	"fmt"
	"path"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/snonux/gonf/resource"
)

// freeBSDNoUserExit is sysexits.h's EX_NOUSER. pw(8) uses it when usershow or
// groupshow cannot find the requested database entry.
const freeBSDNoUserExit = 67

// FreeBSD reconciles DesiredUser values with FreeBSD's pw(8) utility. It
// creates only missing groups and users, and adds only missing supplementary
// memberships. It never deletes an account or group, removes a membership, or
// changes an existing account's primary group, shell, or login class. An
// existing account's home field changes only when DesiredUser.ManageHome opts
// in, and then only via pw usermod -d without -m.
type FreeBSD struct {
	run Runner
}

type freeBSDUser struct {
	name   string
	record string
}

// NewFreeBSD constructs a FreeBSD backend with runner. A nil runner uses the
// normal bounded command runner.
func NewFreeBSD(runner Runner) FreeBSD {
	return FreeBSD{run: defaultRunner(runner)}
}

// Ensure converges want without destructive account operations.
func (b FreeBSD) Ensure(want DesiredUser) error {
	if err := want.Validate(); err != nil {
		return err
	}
	if err := validateFreeBSDHome(want); err != nil {
		return err
	}
	user, exists, err := b.user(want.Name)
	if err != nil {
		return err
	}
	if exists {
		return b.ensureExistingUser(user, want)
	}
	if want.System {
		return fmt.Errorf("user %q: system accounts are not supported on FreeBSD", want.Name)
	}
	return b.ensureMissingUser(want)
}

// EnsureFreeBSD reconciles want with the default bounded command runner.
func EnsureFreeBSD(want DesiredUser) error {
	return NewFreeBSD(nil).Ensure(want)
}

// ensureExistingUser adds missing memberships and then, only when opted in,
// converges the passwd home field. user.record is the pw usershow line read
// before any mutation; a membership update never alters the home field.
func (b FreeBSD) ensureExistingUser(user freeBSDUser, want DesiredUser) error {
	if err := b.addMissingMemberships(user, want); err != nil {
		return err
	}
	return b.ensureHomeField(user, want)
}

// ensureHomeField rewrites only the passwd home field. pw usermod -d without
// -m neither creates, moves, nor chowns the directory, and leaves the
// password, lock state, shell, and login class untouched.
func (b FreeBSD) ensureHomeField(user freeBSDUser, want DesiredUser) error {
	if !want.ManageHome {
		return nil
	}
	current, err := passwdHome(user.record, want.Name, freeBSDHomeField)
	if err != nil || current == want.Home {
		return err
	}
	return b.runMutation("User["+want.Name+"]", "pw", "usermod", "-n", want.Name, "-d", want.Home)
}

func (b FreeBSD) addMissingMemberships(user freeBSDUser, want DesiredUser) error {
	desired := want.Supplementary()
	if len(desired) == 0 {
		return nil
	}

	// pw usermod -G replaces every secondary membership. groupshow -a is the
	// complete local group database that pw will rewrite, unlike id -Gn whose
	// output is bounded by the platform's supplementary-group limit. Read it
	// before any mutation so the replacement list retains every local secondary
	// membership while excluding the real primary group as pw(8) requires.
	primary, current, err := b.secondaryGroups(user)
	if err != nil {
		return err
	}
	for _, group := range desired {
		if group == primary {
			continue
		}
		if err := b.ensureGroup(group); err != nil {
			return err
		}
	}
	groups := addSecondaryGroups(current, desired, primary)
	if slices.Equal(groups, current) {
		return nil
	}
	return b.runMutation("User["+want.Name+"]", "pw", "usermod", "-n", want.Name, "-G", strings.Join(groups, ","))
}

func (b FreeBSD) ensureMissingUser(want DesiredUser) error {
	for _, group := range creationGroups(want) {
		if err := b.ensureGroup(group); err != nil {
			return err
		}
	}
	return b.addUser(want)
}

func (b FreeBSD) ensureGroup(group string) error {
	exists, err := b.groupExists(group)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	return b.runMutation("Group["+group+"]", "pw", "groupadd", "-n", group)
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
	return b.runMutation("User["+want.Name+"]", "pw", args...)
}

func (b FreeBSD) user(name string) (freeBSDUser, bool, error) {
	stdout, stderr, code, err := b.run("pw", "usershow", "-n", name)
	if err != nil {
		return freeBSDUser{}, false, fmt.Errorf("pw usershow -n %s: %w", name, err)
	}
	switch code {
	case 0:
		return freeBSDUser{name: name, record: stdout}, true, nil
	case freeBSDNoUserExit:
		return freeBSDUser{}, false, nil
	default:
		return freeBSDUser{}, false, commandError("pw", []string{"usershow", "-n", name}, code, stdout, stderr)
	}
}

func (b FreeBSD) groupExists(group string) (bool, error) {
	stdout, stderr, code, err := b.run("pw", "groupshow", "-n", group)
	if err != nil {
		return false, fmt.Errorf("pw groupshow -n %s: %w", group, err)
	}
	switch code {
	case 0:
		return true, nil
	case freeBSDNoUserExit:
		return false, nil
	default:
		return false, commandError("pw", []string{"groupshow", "-n", group}, code, stdout, stderr)
	}
}

func (b FreeBSD) secondaryGroups(user freeBSDUser) (string, []string, error) {
	fields := strings.Split(strings.TrimSpace(user.record), ":")
	if len(fields) < 4 || fields[0] != user.name {
		return "", nil, fmt.Errorf("pw usershow -n %s returned malformed passwd entry", user.name)
	}
	gid, err := strconv.ParseUint(fields[3], 10, 32)
	if err != nil {
		return "", nil, fmt.Errorf("pw usershow -n %s returned invalid primary gid %q: %w", user.name, fields[3], err)
	}
	stdout, stderr, code, runErr := b.run("pw", "groupshow", "-a")
	if runErr != nil {
		return "", nil, fmt.Errorf("pw groupshow -a: %w", runErr)
	}
	if code != 0 {
		return "", nil, commandError("pw", []string{"groupshow", "-a"}, code, stdout, stderr)
	}

	var primary string
	groups := make(map[string]struct{})
	for _, line := range strings.Split(strings.TrimSpace(stdout), "\n") {
		if line == "" {
			continue
		}
		groupFields := strings.SplitN(line, ":", 4)
		if len(groupFields) != 4 {
			return "", nil, fmt.Errorf("pw groupshow -a returned malformed group entry %q", line)
		}
		if err := validateName("group returned by pw groupshow", groupFields[0]); err != nil {
			return "", nil, err
		}
		groupID, parseErr := strconv.ParseUint(groupFields[2], 10, 32)
		if parseErr != nil {
			return "", nil, fmt.Errorf("pw groupshow -a returned invalid gid %q: %w", groupFields[2], parseErr)
		}
		if groupID == gid {
			primary = groupFields[0]
			continue
		}
		if slices.Contains(strings.Split(groupFields[3], ","), user.name) {
			groups[groupFields[0]] = struct{}{}
		}
	}
	if primary == "" {
		return "", nil, fmt.Errorf("pw groupshow -a did not contain primary gid %d for user %q", gid, user.name)
	}
	return primary, sortedGroups(groups), nil
}

func (b FreeBSD) runAction(command string, args ...string) error {
	stdout, stderr, code, err := b.run(command, args...)
	if err != nil {
		return fmt.Errorf("%s %s: %w", command, strings.Join(args, " "), err)
	}
	if code != 0 {
		return commandError(command, args, code, stdout, stderr)
	}
	return nil
}

func (b FreeBSD) runMutation(id, command string, args ...string) error {
	return resource.Mutate(id, "run "+command+" "+strings.Join(args, " "), func() error {
		return b.runAction(command, args...)
	})
}

func validateFreeBSDHome(want DesiredUser) error {
	if !want.CreateHome || want.Home == "" {
		return nil
	}
	if !path.IsAbs(want.Home) {
		return fmt.Errorf("user %q: home must be absolute when CreateHome is set", want.Name)
	}
	if path.Clean(want.Home) == "/" {
		return fmt.Errorf("user %q: home must not be the root directory when CreateHome is set", want.Name)
	}
	return nil
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
