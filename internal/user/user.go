// Package user defines the backend-neutral description of a local account.
// It deliberately contains no resource registration or plan-wire concerns;
// those live in the public resource/user package.
package user

import (
	"fmt"
	"path"
	"slices"
	"sort"
	"strings"
)

// DesiredUser describes an account that a platform backend should make
// available. Optional creation attributes are applied only while creating a
// missing account. Existing accounts are never removed by this model; by
// default backends may only add missing supplementary group memberships. The
// single explicit exception is ManageHome, which lets a backend rewrite the
// passwd home field of an existing account (never its contents).
type DesiredUser struct {
	// Name is the local account name.
	Name string
	// PrimaryGroup is the account's primary group. When set, the backend makes
	// sure the group exists before creating the account.
	PrimaryGroup string
	// SupplementaryGroups are memberships that must be added. Memberships not
	// listed here are retained.
	SupplementaryGroups []string
	// Home is the account's home directory when creating it. Empty selects the
	// platform default. Setting Home alone never creates the directory. When
	// ManageHome is set, Home is also the value an existing account's passwd
	// home field converges to.
	Home string
	// CreateHome creates Home (or the platform default home when Home is empty)
	// while creating a missing account. It defaults to false, so the zero-value
	// DesiredUser never creates a home directory.
	CreateHome bool
	// Shell is the account's login shell when creating it. Empty selects the
	// platform default.
	Shell string
	// LoginClass selects the platform login class when creating the account.
	// Empty selects the platform default.
	LoginClass string
	// System requests a system account when creating it.
	System bool
	// ManageHome opts in to converging the passwd home field of an account
	// that already exists to Home. It only rewrites that one field (usermod -d
	// without -m, or pw usermod -d): it never moves, creates, deletes, or
	// chowns the directory, and never touches passwords, locks, the shell, or
	// memberships. It requires Home to be an absolute, clean path.
	ManageHome bool
}

// Validate reports malformed account or group names before a backend executes
// any command. Home and Shell are passed as individual argv values and may
// contain spaces; names cannot because group membership probes are
// whitespace-delimited and Rocky's group option is comma-delimited.
func (u DesiredUser) Validate() error {
	if err := validateName("user", u.Name); err != nil {
		return err
	}
	if u.PrimaryGroup != "" {
		if err := validateName("primary group", u.PrimaryGroup); err != nil {
			return err
		}
	}
	for _, group := range u.SupplementaryGroups {
		if err := validateName("supplementary group", group); err != nil {
			return err
		}
	}
	if strings.ContainsRune(u.Home, '\x00') {
		return fmt.Errorf("user %q: home contains NUL", u.Name)
	}
	if strings.ContainsRune(u.Shell, '\x00') {
		return fmt.Errorf("user %q: shell contains NUL", u.Name)
	}
	if strings.ContainsRune(u.LoginClass, '\x00') {
		return fmt.Errorf("user %q: login class contains NUL", u.Name)
	}
	if u.ManageHome {
		return ValidateManagedHome(u.Name, u.Home)
	}
	return nil
}

// ValidateManagedHome checks the contract of an opted-in existing-account
// home field. usermod and pw store the value exactly as given, so gonf only
// accepts values that are unambiguous and safe to store:
//   - set, because there is no platform default to converge an existing
//     account to;
//   - absolute, because a relative passwd home has no defined meaning;
//   - clean (no trailing '/', '.', '..' or repeated '/' segments), so each
//     directory has exactly one canonical spelling in the passwd database and
//     a recipe cannot record an alias of the directory it means;
//   - free of ':' (the passwd field separator), line breaks (the record
//     separator), and NUL, which the passwd format and the tools' argv cannot
//     carry safely.
//
// It is exported so the plan wire can reject a bad recipe at record time
// rather than on the destination.
func ValidateManagedHome(name, home string) error {
	switch {
	case home == "":
		return fmt.Errorf("user %q: managing an existing home requires a home directory", name)
	case !path.IsAbs(home):
		return fmt.Errorf("user %q: managed home %q must be absolute", name, home)
	case path.Clean(home) != home:
		return fmt.Errorf("user %q: managed home %q must be a clean path (want %q)", name, home, path.Clean(home))
	case strings.ContainsAny(home, ":\n\r\x00"):
		return fmt.Errorf("user %q: managed home %q contains ':', a line break, or NUL", name, home)
	}
	return nil
}

// getentHomeField is the zero-based home column of getent passwd output
// (name:pw:uid:gid:gecos:home:shell).
const getentHomeField = 5

// freeBSDHomeField is the zero-based home column of pw usershow output, which
// uses the master.passwd layout
// (name:pw:uid:gid:class:change:expire:gecos:home:shell).
const freeBSDHomeField = 8

// passwdHome returns the home field of one passwd-format record for name.
// homeField is getentHomeField or freeBSDHomeField. A record with too few
// fields, or one that belongs to a different account, is an error, so a
// surprising probe never triggers a home rewrite. The account mismatch gets
// its own message: glibc getent passwd treats an all-digit key as a UID, so
// a numeric account name can return another account's record, which is not a
// malformed database but a lookup that answered a different question.
func passwdHome(record, name string, homeField int) (string, error) {
	line, _, _ := strings.Cut(strings.TrimSpace(record), "\n")
	fields := strings.Split(line, ":")
	if len(fields) <= homeField {
		return "", fmt.Errorf("user %q: malformed passwd entry %q", name, line)
	}
	if fields[0] != name {
		return "", fmt.Errorf("user %q: account lookup returned the entry of account %q (a numeric name may have been resolved as a UID); refusing to change the home", name, fields[0])
	}
	return fields[homeField], nil
}

// Groups returns every group that must exist for this account, sorted and
// de-duplicated. The returned slice is safe for callers to modify.
func (u DesiredUser) Groups() []string {
	if u.PrimaryGroup == "" && len(u.SupplementaryGroups) == 0 {
		return nil
	}
	groups := make([]string, 0, len(u.SupplementaryGroups)+1)
	if u.PrimaryGroup != "" {
		groups = append(groups, u.PrimaryGroup)
	}
	groups = append(groups, u.SupplementaryGroups...)
	sort.Strings(groups)
	return slices.Compact(groups)
}

// Supplementary returns sorted, de-duplicated supplementary group names. A
// primary group repeated in SupplementaryGroups is removed because usermod's
// supplementary-group operation must not be used to manage primary groups.
func (u DesiredUser) Supplementary() []string {
	groups := append([]string(nil), u.SupplementaryGroups...)
	sort.Strings(groups)
	groups = slices.Compact(groups)
	if u.PrimaryGroup == "" {
		return groups
	}
	return slices.DeleteFunc(groups, func(group string) bool { return group == u.PrimaryGroup })
}

func validateName(kind, name string) error {
	if name == "" {
		return fmt.Errorf("%s name is empty", kind)
	}
	if strings.HasPrefix(name, "-") {
		return fmt.Errorf("%s name %q starts with -", kind, name)
	}
	if strings.ContainsAny(name, "\x00, \t\n\r\v\f") {
		return fmt.Errorf("%s name %q contains whitespace, comma, or NUL", kind, name)
	}
	return nil
}
