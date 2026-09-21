package user

import "strings"

// Linux reconciles DesiredUser values on any Linux host whose account tools
// are shadow-utils compatible: getent(1), id(1) with GNU long options, and
// shadow-utils groupadd/useradd/usermod with --long options. resource/user
// selects it for every GOOS=linux destination (Rocky, Fedora, Debian, ...), so
// nothing in it may depend on a particular distribution. It was named Rocky
// before task l72, and its refusals now name the platform "Linux".
//
// It creates only missing groups and users, and adds only missing
// supplementary memberships. It never deletes an account or group, removes a
// membership, or changes an existing account's primary group or shell. An
// existing account's home field changes only when DesiredUser.ManageHome
// opts in, and then only via usermod --home without --move-home.
type Linux struct {
	commands
}

// NewLinux constructs a Linux backend with runner. A nil runner uses the
// normal bounded command runner.
func NewLinux(runner Runner) Linux {
	return Linux{commands{run: defaultRunner(runner)}}
}

// Ensure converges want without destructive account operations, reporting
// account mutations under id. r is a value copy, so setting userID here
// never leaks into another call.
func (r Linux) Ensure(id string, want DesiredUser) error {
	r.userID = id
	return ensure(r, want)
}

// Capabilities declares the shadow-utils contract: useradd --system creates
// system accounts, and there is no login-class concept, so a login class is
// refused before any command.
func (Linux) Capabilities() Capabilities {
	return Capabilities{Platform: "Linux", SystemAccount: true}
}

// lookupUser reads the account's getent passwd record.
func (r Linux) lookupUser(name string) (string, bool, error) {
	return r.getent("passwd", name)
}

// ensureExistingUser adds missing memberships and then, only when opted in,
// converges the passwd home field. record is the getent passwd line read
// before any mutation; membership changes never alter the home field, so it
// stays accurate for the home comparison.
func (r Linux) ensureExistingUser(record string, want DesiredUser) error {
	if err := ensureEach(want.Supplementary(), r.ensureGroup); err != nil {
		return err
	}
	if err := r.addMissingMemberships(want); err != nil {
		return err
	}
	return r.ensureHomeField(record, want)
}

// ensureHomeField rewrites only the passwd home field. shadow-utils usermod
// without --move-home neither creates, moves, nor chowns the directory, and
// leaves the password, lock state, and shell untouched. usermod may refuse
// while the account has running processes; that error is returned as-is
// rather than stopping anything.
func (r Linux) ensureHomeField(record string, want DesiredUser) error {
	if !want.ManageHome {
		return nil
	}
	current, err := passwdHome(record, want.Name, getentHomeField)
	if err != nil || current == want.Home {
		return err
	}
	return r.mutateUser("usermod", "--home", want.Home, "--", want.Name)
}

// ensureMissingUser creates every missing requested group, then the account.
func (r Linux) ensureMissingUser(want DesiredUser) error {
	if err := ensureEach(want.Groups(), r.ensureGroup); err != nil {
		return err
	}
	return r.addUser(want)
}

func (r Linux) ensureGroup(group string) error {
	return r.ensureGroupWith(r.getentGroupExists, group, "groupadd", "--", group)
}

func (r Linux) addUser(want DesiredUser) error {
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
	return r.mutateUser("useradd", args...)
}

// addMissingMemberships appends only the requested groups id(1) does not
// already report; usermod --append never drops an existing membership.
func (r Linux) addMissingMemberships(want DesiredUser) error {
	desired := want.Supplementary()
	if len(desired) == 0 {
		return nil
	}
	current, err := r.idGroups("--groups", "--name", want.Name)
	if err != nil {
		return err
	}
	missing := missingGroups(desired, current)
	if len(missing) == 0 {
		return nil
	}
	return r.mutateUser("usermod", "--append", "--groups", strings.Join(missing, ","), "--", want.Name)
}
