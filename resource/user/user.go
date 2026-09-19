// Package user implements an additive-only local user resource.
package user

import (
	"fmt"
	"runtime"

	internaluser "github.com/snonux/gonf/internal/user"
	"github.com/snonux/gonf/resource"
	"github.com/snonux/gonf/resource/embed"
	opt "github.com/snonux/gonf/resource/options"
)

// User ensures a local account exists. Creation attributes are used only for
// a missing account; for an existing account only missing supplementary group
// memberships may be added. It never removes or rewrites accounts, groups, or
// memberships.
type User struct {
	embed.DependsOn
	name                string
	primaryGroup        string
	supplementaryGroups []string
	home                string
	createHome          bool
	shell               string
	loginClass          string
	system              bool
}

var (
	_ opt.Dependable             = (*User)(nil)
	_ opt.Grouped                = (*User)(nil)
	_ opt.Homeable               = (*User)(nil)
	_ opt.CreateHomeable         = (*User)(nil)
	_ opt.Shellable              = (*User)(nil)
	_ opt.Classable              = (*User)(nil)
	_ opt.Systemable             = (*User)(nil)
	_ opt.SupplementaryGroupable = (*User)(nil)
)

// ensureCurrent is a variable only so this package can verify the public
// resource and plan handler without accessing a host account database.
var ensureCurrent = ensureForPlatform

var (
	ensureRocky   = internaluser.EnsureRocky
	ensureOpenBSD = internaluser.EnsureOpenBSD
	ensureFreeBSD = internaluser.EnsureFreeBSD
	ensureNetBSD  = internaluser.EnsureNetBSD
)

// Present registers a local user that should exist.
func Present(name string, opts ...opt.LocalUserOption) resource.Resource {
	u := newUser(name, opts)
	r := resource.Register("User", u.name, u, u.DependsOn.IDs...)
	resource.RecordPlanDraft(u.planDraft(r.ID()))
	return r
}

// Ensure builds and applies a local user resource without registering it or
// recording a plan draft.
func Ensure(name string, opts ...opt.LocalUserOption) error {
	return newUser(name, opts).apply()
}

// SetGroup sets the creation-time primary group.
func (u *User) SetGroup(group string) { u.primaryGroup = group }

// SetHome sets the creation-time home directory.
func (u *User) SetHome(home string) { u.home = home }

// SetCreateHome requests creation of the home directory for a missing user.
func (u *User) SetCreateHome() { u.createHome = true }

// SetShell sets the creation-time login shell.
func (u *User) SetShell(shell string) { u.shell = shell }

// SetLoginClass sets the creation-time platform login class.
func (u *User) SetLoginClass(class string) { u.loginClass = class }

// SetSystem requests a system account when creating a missing user.
func (u *User) SetSystem() { u.system = true }

// AddSupplementaryGroups adds desired supplementary memberships.
func (u *User) AddSupplementaryGroups(groups ...string) {
	u.supplementaryGroups = append(u.supplementaryGroups, groups...)
}

// Apply runs user reconciliation directly for the legacy resource path.
func (u *User) Apply() error { return u.apply() }

func newUser(name string, opts []opt.LocalUserOption) *User {
	u := &User{name: name}
	for _, option := range opts {
		option.Apply(u)
	}
	return u
}

func (u *User) planDraft(id string) resource.PlanDraft {
	return resource.PlanDraft{
		Kind:                "user",
		ID:                  id,
		Name:                u.name,
		PrimaryGroup:        u.primaryGroup,
		SupplementaryGroups: append([]string(nil), u.supplementaryGroups...),
		Home:                u.home,
		CreateHome:          u.createHome,
		Shell:               u.shell,
		LoginClass:          u.loginClass,
		System:              u.system,
		Deps:                u.DependsOn.SortedIDs(),
	}
}

func (u *User) apply() error {
	want := u.desired()
	if err := want.Validate(); err != nil {
		return err
	}
	if err := ensureCurrent(want); err != nil {
		return err
	}
	// The backend uses Mutate for every account creation or membership update,
	// which records User[name] as changed. A converged account has no mutation
	// to report, so add the standard StatusOK outcome expected from a managed
	// resource without duplicating a changed note.
	id := "User[" + want.Name + "]"
	if !resource.AnyChanged(id) {
		resource.NoteResult(id, false)
	}
	return nil
}

func (u *User) desired() internaluser.DesiredUser {
	return internaluser.DesiredUser{
		Name:                u.name,
		PrimaryGroup:        u.primaryGroup,
		SupplementaryGroups: append([]string(nil), u.supplementaryGroups...),
		Home:                u.home,
		CreateHome:          u.createHome,
		Shell:               u.shell,
		LoginClass:          u.loginClass,
		System:              u.system,
	}
}

func ensureForPlatform(want internaluser.DesiredUser) error {
	return ensureForGOOS(runtime.GOOS, want)
}

func ensureForGOOS(goos string, want internaluser.DesiredUser) error {
	switch goos {
	case "linux":
		return ensureRocky(want)
	case "openbsd":
		return ensureOpenBSD(want)
	case "freebsd":
		return ensureFreeBSD(want)
	case "netbsd":
		return ensureNetBSD(want)
	default:
		return fmt.Errorf("user %q: unsupported operating system %s", want.Name, goos)
	}
}
