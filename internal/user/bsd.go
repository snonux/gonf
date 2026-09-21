package user

import (
	"fmt"
	"strings"

	gonfexec "github.com/snonux/gonf/internal/exec"
	"github.com/snonux/gonf/resource"
)

// maxBSDSupplementaryGroups is the portable useradd/usermod -G limit. NetBSD
// documents a maximum of 16 groups; rejecting a larger desired set prevents a
// platform utility from silently truncating it.
const maxBSDSupplementaryGroups = 16

// OpenBSD reconciles DesiredUser values using OpenBSD's user-management
// utilities. It creates only missing groups and users, and adds only missing
// supplementary memberships. It never deletes an account or group, removes a
// membership, or changes an existing account's primary group, shell, or login
// class. An existing account's home field changes only when
// DesiredUser.ManageHome opts in, and then only via usermod -d without -m.
type OpenBSD struct {
	run Runner
}

// NetBSD reconciles DesiredUser values using NetBSD's user-management
// utilities. It creates only missing groups and users, and adds only missing
// supplementary memberships. It never deletes an account or group, removes a
// membership, or changes an existing account's primary group, shell, or login
// class. An existing account's home field changes only when
// DesiredUser.ManageHome opts in, and then only via usermod -d without -m.
type NetBSD struct {
	run Runner
}

type bsd struct {
	run Runner
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
	return bsd(b).Ensure(want)
}

// Ensure converges want without destructive account operations.
func (b NetBSD) Ensure(want DesiredUser) error {
	return bsd(b).Ensure(want)
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
func (b bsd) ensureExistingUser(record string, want DesiredUser) error {
	for _, group := range want.Supplementary() {
		if err := b.ensureGroup(group); err != nil {
			return err
		}
	}
	if err := b.addMissingMemberships(want); err != nil {
		return err
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

func (b bsd) addMissingMemberships(want DesiredUser) error {
	desired := want.Supplementary()
	if len(desired) == 0 {
		return nil
	}
	current, err := b.userGroups(want.Name)
	if err != nil {
		return err
	}
	missing := make([]string, 0, len(desired))
	for _, group := range desired {
		if !current[group] {
			missing = append(missing, group)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return b.runMutation("User["+want.Name+"]", "usermod", "-G", strings.Join(missing, ","), want.Name)
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
