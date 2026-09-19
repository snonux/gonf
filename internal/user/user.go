// Package user defines the backend-neutral description of a local account.
// It deliberately contains no resource registration or plan-wire concerns;
// those belong to a later public DSL.
package user

import (
	"fmt"
	"slices"
	"sort"
	"strings"
)

// DesiredUser describes an account that a platform backend should make
// available. Optional creation attributes are applied only while creating a
// missing account. Existing accounts are never removed or rewritten by this
// model; backends may only add missing supplementary group memberships.
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
	// platform default. Setting Home alone never creates the directory.
	Home string
	// CreateHome creates Home (or the platform default home when Home is empty)
	// while creating a missing account. It defaults to false, so the zero-value
	// DesiredUser never creates a home directory.
	CreateHome bool
	// Shell is the account's login shell when creating it. Empty selects the
	// platform default.
	Shell string
	// System requests a system account when creating it.
	System bool
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
	return nil
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
