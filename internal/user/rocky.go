package user

import (
	"fmt"
	"strings"

	gonfexec "github.com/snonux/gonf/internal/exec"
	"github.com/snonux/gonf/resource"
)

// Runner executes one command and returns its stdout, stderr, exit code, and
// start error. It is intentionally small so the Rocky backend can be tested
// without a host account database.
type Runner func(name string, args ...string) (stdout, stderr string, exitCode int, err error)

// Rocky reconciles DesiredUser values on Rocky Linux using shadow-utils.
// It creates only missing groups and users, and adds only missing
// supplementary memberships. It never deletes an account or group, removes a
// membership, or changes an existing account's primary group, home, or shell.
type Rocky struct {
	run Runner
}

// NewRocky constructs a Rocky backend with runner. A nil runner uses the
// normal bounded command runner.
func NewRocky(runner Runner) Rocky {
	if runner == nil {
		runner = gonfexec.Run
	}
	return Rocky{run: runner}
}

// Ensure converges want without destructive account operations.
func (r Rocky) Ensure(want DesiredUser) error {
	if err := want.Validate(); err != nil {
		return err
	}
	if want.LoginClass != "" {
		return fmt.Errorf("user %q: login classes are not supported on Rocky Linux", want.Name)
	}
	exists, err := r.userExists(want.Name)
	if err != nil {
		return err
	}
	if exists {
		return r.ensureExistingUser(want)
	}
	return r.ensureMissingUser(want)
}

func (r Rocky) ensureExistingUser(want DesiredUser) error {
	for _, group := range want.Supplementary() {
		if err := r.ensureGroup(group); err != nil {
			return err
		}
	}
	return r.addMissingMemberships(want)
}

func (r Rocky) ensureMissingUser(want DesiredUser) error {
	for _, group := range want.Groups() {
		if err := r.ensureGroup(group); err != nil {
			return err
		}
	}
	return r.addUser(want)
}

func (r Rocky) ensureGroup(group string) error {
	exists, err := r.groupExists(group)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	return r.runMutation("Group["+group+"]", "groupadd", "--", group)
}

func (r Rocky) addUser(want DesiredUser) error {
	args := []string{"--no-create-home"}
	if want.CreateHome {
		args[0] = "--create-home"
	}
	if want.System {
		args = append(args, "--system")
	}
	if want.PrimaryGroup != "" {
		args = append(args, "--gid", want.PrimaryGroup)
	}
	if groups := want.Supplementary(); len(groups) > 0 {
		args = append(args, "--groups", strings.Join(groups, ","))
	}
	if want.Home != "" {
		args = append(args, "--home", want.Home)
	}
	if want.Shell != "" {
		args = append(args, "--shell", want.Shell)
	}
	args = append(args, "--", want.Name)
	return r.runMutation("User["+want.Name+"]", "useradd", args...)
}

func (r Rocky) addMissingMemberships(want DesiredUser) error {
	desired := want.Supplementary()
	if len(desired) == 0 {
		return nil
	}
	current, err := r.userGroups(want.Name)
	if err != nil {
		return err
	}
	missing := make([]string, 0, len(want.SupplementaryGroups))
	for _, group := range desired {
		if !current[group] {
			missing = append(missing, group)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return r.runMutation("User["+want.Name+"]", "usermod", "--append", "--groups", strings.Join(missing, ","), "--", want.Name)
}

func (r Rocky) groupExists(group string) (bool, error) {
	return r.getentExists("group", group)
}

func (r Rocky) userExists(name string) (bool, error) {
	return r.getentExists("passwd", name)
}

func (r Rocky) getentExists(database, key string) (bool, error) {
	_, stderr, code, err := r.run("getent", database, key)
	if err != nil {
		return false, fmt.Errorf("getent %s %s: %w", database, key, err)
	}
	switch code {
	case 0:
		return true, nil
	case 2:
		return false, nil
	default:
		return false, commandError("getent", []string{database, key}, code, "", stderr)
	}
}

func (r Rocky) userGroups(name string) (map[string]bool, error) {
	stdout, stderr, code, err := r.run("id", "--groups", "--name", name)
	if err != nil {
		return nil, fmt.Errorf("id --groups --name %s: %w", name, err)
	}
	if code != 0 {
		return nil, commandError("id", []string{"--groups", "--name", name}, code, stdout, stderr)
	}
	groups := make(map[string]bool)
	for _, group := range strings.Fields(stdout) {
		groups[group] = true
	}
	return groups, nil
}

func (r Rocky) runAction(command string, args ...string) error {
	stdout, stderr, code, err := r.run(command, args...)
	if err != nil {
		return fmt.Errorf("%s %s: %w", command, strings.Join(args, " "), err)
	}
	if code != 0 {
		return commandError(command, args, code, stdout, stderr)
	}
	return nil
}

func (r Rocky) runMutation(id, command string, args ...string) error {
	return resource.Mutate(id, "run "+command+" "+strings.Join(args, " "), func() error {
		return r.runAction(command, args...)
	})
}

func commandError(command string, args []string, code int, stdout, stderr string) error {
	return fmt.Errorf("%s %s failed (exit %d): %s%s", command, strings.Join(args, " "), code, stdout, stderr)
}

// EnsureRocky reconciles want with the default bounded command runner.
func EnsureRocky(want DesiredUser) error {
	return NewRocky(nil).Ensure(want)
}
