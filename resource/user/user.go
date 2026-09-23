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

// resourceType is the User resource's type label: the one input, with the
// account name, to its resource.FormatID identifier.
const resourceType = "User"

var (
	_ opt.Dependable             = (*User)(nil)
	_ opt.Grouped                = (*User)(nil)
	_ opt.Homeable               = (*User)(nil)
	_ opt.CreateHomeable         = (*User)(nil)
	_ opt.Shellable              = (*User)(nil)
	_ opt.Classable              = (*User)(nil)
	_ opt.Systemable             = (*User)(nil)
	_ opt.SupplementaryGroupable = (*User)(nil)
	_ opt.HomeManageable         = (*User)(nil)
	_ opt.MisuseReporter         = (*User)(nil)
)

// User ensures a local account exists. Creation attributes are used only for
// a missing account; for an existing account only missing supplementary group
// memberships may be added and, when WithManageHome opts in, the passwd home
// field may be rewritten to WithHome. It never removes accounts, groups, or
// memberships, and never moves, creates, or chowns an existing home.
type User struct {
	embed.DependsOn
	embed.Misuse
	name                string
	primaryGroup        string
	supplementaryGroups []string
	home                string
	createHome          bool
	shell               string
	loginClass          string
	system              bool
	manageHome          bool
	// backend converges the desired account on this host. newUser selects
	// the backend for the running GOOS; tests inject a fake through
	// newUserWith instead of swapping package-level variables.
	backend internaluser.Backend
}

// unsupportedBackend stands in for a GOOS without a user backend: it
// declares no capabilities and refuses every Ensure, naming the GOOS.
type unsupportedBackend struct{ goos string }

// newUser builds a User that converges through the backend for the running
// GOOS.
func newUser(name string, opts []opt.LocalUserOption) *User {
	return newUserWith(backendForGOOS(runtime.GOOS, nil), name, opts)
}

// newUserWith builds a User that converges through backend. An option misuse
// is left in its embed.Misuse for the caller to check.
func newUserWith(backend internaluser.Backend, name string, opts []opt.LocalUserOption) *User {
	u := &User{name: name, backend: backend}
	for _, option := range opts {
		option.Apply(u)
	}
	return u
}

// Present registers a local user that should exist. An option misuse is
// reported as a declaration error (resource.Refuse) and nothing is registered.
func Present(name string, opts ...opt.LocalUserOption) resource.Resource {
	u := newUser(name, opts)
	if err := u.MisuseErr(); err != nil {
		return resource.Refuse(resourceType, name, err)
	}
	r, ok := resource.Register(resourceType, u.name, u, u.DependsOn.IDs...)
	if ok {
		resource.RecordPlanDraft(u.planDraft(r.ID()))
	}
	return r
}

// Ensure builds and applies a local user resource without registering it or
// recording a plan draft.
func Ensure(name string, opts ...opt.LocalUserOption) error {
	u := newUser(name, opts)
	if err := u.MisuseErr(); err != nil {
		return err
	}
	return u.apply()
}

// SetGroup sets the creation-time primary group.
func (u *User) SetGroup(group string) { u.primaryGroup = group }

// SetHome sets the creation-time home directory. With WithManageHome it is
// also the value an existing account's passwd home field converges to.
func (u *User) SetHome(home string) { u.home = home }

// SetCreateHome requests creation of the home directory for a missing user.
func (u *User) SetCreateHome() { u.createHome = true }

// SetShell sets the creation-time login shell.
func (u *User) SetShell(shell string) { u.shell = shell }

// SetLoginClass sets the creation-time platform login class.
func (u *User) SetLoginClass(class string) { u.loginClass = class }

// SetSystem requests a system account when creating a missing user.
func (u *User) SetSystem() { u.system = true }

// SetManageHome opts in to converging an existing account's home field.
func (u *User) SetManageHome() { u.manageHome = true }

// AddSupplementaryGroups adds desired supplementary memberships.
func (u *User) AddSupplementaryGroups(groups ...string) {
	u.supplementaryGroups = append(u.supplementaryGroups, groups...)
}

// Ensure reports that goos has no user backend.
func (b unsupportedBackend) Ensure(_ string, want internaluser.DesiredUser) error {
	return fmt.Errorf("user %q: unsupported operating system %s", want.Name, b.goos)
}

// Capabilities returns zero capabilities (only Platform names the GOOS);
// Ensure refuses everything regardless.
func (b unsupportedBackend) Capabilities() internaluser.Capabilities {
	return internaluser.Capabilities{Platform: b.goos}
}

// planDraft records u as a "user" plan draft under id. Every user-exclusive
// field travels in Payload (see Payload, task w62 Layer 1).
func (u *User) planDraft(id string) resource.PlanDraft {
	return resource.PlanDraft{
		Kind: "user",
		ID:   id,
		Name: u.name,
		Payload: Payload{
			PrimaryGroup:        u.primaryGroup,
			SupplementaryGroups: append([]string(nil), u.supplementaryGroups...),
			Home:                u.home,
			CreateHome:          u.createHome,
			Shell:               u.shell,
			LoginClass:          u.loginClass,
			System:              u.system,
			ManageHome:          u.manageHome,
		},
		Deps: u.DependsOn.SortedIDs(),
	}
}

func (u *User) apply() error {
	want := u.desired()
	if err := want.Validate(); err != nil {
		return err
	}
	// id is the same identifier Present registered (Resource.ID uses
	// resource.FormatID too). The backend reports every account creation,
	// membership update and opted-in home-field update under it via Mutate.
	id := resource.FormatID(resourceType, want.Name)
	if err := u.backend.Ensure(id, want); err != nil {
		return err
	}
	// A converged account has no mutation to report, so add the standard
	// StatusOK outcome expected from a managed resource without duplicating a
	// changed note.
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
		ManageHome:          u.manageHome,
	}
}

// backendForGOOS returns the backend that converges an account on goos:
// internaluser.ForGOOS's backend, running commands through run (nil selects
// the normal bounded runner). An unsupported goos still yields a backend,
// which reports the operating system when applied, so a recipe can be
// recorded on any controller.
func backendForGOOS(goos string, run internaluser.Runner) internaluser.Backend {
	if backend, ok := internaluser.ForGOOS(goos, run); ok {
		return backend
	}
	return unsupportedBackend{goos: goos}
}
