package user

import (
	"fmt"
	"strings"

	gonfexec "github.com/snonux/gonf/internal/exec"
	"github.com/snonux/gonf/resource"
)

// Runner executes one command and returns its stdout, stderr, exit code, and
// start error. It is intentionally small so every backend can be tested
// without a host account database.
type Runner func(name string, args ...string) (stdout, stderr string, exitCode int, err error)

// defaultRunner returns runner, or the normal bounded command runner when
// runner is nil.
func defaultRunner(runner Runner) Runner {
	if runner == nil {
		return gonfexec.Run
	}
	return runner
}

// commands is the command plumbing shared by every backend: it runs probes
// and account mutations through the injected Runner and formats their
// failures identically, so a backend only contributes its OS-specific argv
// and output parsing.
type commands struct {
	run Runner
	// userID is the caller's resource ID for the account being converged.
	// Each backend's Ensure sets it on its own value copy, so every account
	// mutation is reported under exactly the ID the resource layer
	// registered, and internal/user never spells the ID format itself.
	userID string
}

// probe runs a read-only command and returns its stdout. A start failure is
// wrapped as "<command> <args>: <err>"; a non-zero exit becomes commandError.
func (c commands) probe(command string, args ...string) (string, error) {
	stdout, stderr, code, err := c.run(command, args...)
	if err != nil {
		return "", fmt.Errorf("%s %s: %w", command, strings.Join(args, " "), err)
	}
	if code != 0 {
		return "", commandError(command, args, code, stdout, stderr)
	}
	return stdout, nil
}

// mutate runs one account-changing command under resource.Mutate, which
// records id as changed and suppresses the command in dry-run mode.
func (c commands) mutate(id, command string, args ...string) error {
	return resource.Mutate(id, "run "+command+" "+strings.Join(args, " "), func() error {
		_, err := c.probe(command, args...)
		return err
	})
}

// mutateUser runs an account mutation reported under the caller's user ID.
func (c commands) mutateUser(command string, args ...string) error {
	return c.mutate(c.userID, command, args...)
}

// groupID is the report ID of a group the backend creates. Groups are not
// registered resources; they only share the canonical resource.FormatID
// spelling so reports and AnyChanged callers see one ID format.
func groupID(group string) string { return resource.FormatID("Group", group) }

// getent runs getent(1), used by the Linux and OpenBSD/NetBSD backends, and
// returns the record it printed. Exit status 2 means "key not found" on all
// three platforms. Unlike probe, an unexpected exit reports only stderr.
func (c commands) getent(database, key string) (string, bool, error) {
	stdout, stderr, code, err := c.run("getent", database, key)
	if err != nil {
		return "", false, fmt.Errorf("getent %s %s: %w", database, key, err)
	}
	switch code {
	case 0:
		return stdout, true, nil
	case 2:
		return "", false, nil
	default:
		return "", false, commandError("getent", []string{database, key}, code, "", stderr)
	}
}

// getentGroupExists reports whether the group database has group.
func (c commands) getentGroupExists(group string) (bool, error) {
	_, exists, err := c.getent("group", group)
	return exists, err
}

// idGroups runs id(1) with args (the platform's "group names of NAME" form)
// and returns the whitespace-separated group names it printed.
func (c commands) idGroups(args ...string) (map[string]bool, error) {
	stdout, err := c.probe("id", args...)
	if err != nil {
		return nil, err
	}
	groups := make(map[string]bool)
	for _, group := range strings.Fields(stdout) {
		groups[group] = true
	}
	return groups, nil
}

// missingGroups returns the groups of desired, in order, that current (an
// idGroups result) does not contain.
func missingGroups(desired []string, current map[string]bool) []string {
	missing := make([]string, 0, len(desired))
	for _, group := range desired {
		if !current[group] {
			missing = append(missing, group)
		}
	}
	return missing
}

// ensureGroupWith runs the groupadd argv (command, args) under the group's
// report ID unless exists reports that group is already present.
func (c commands) ensureGroupWith(exists func(string) (bool, error), group, command string, args ...string) error {
	present, err := exists(group)
	if err != nil || present {
		return err
	}
	return c.mutate(groupID(group), command, args...)
}

// ensureEach calls ensure for every group in order and stops at the first
// error, so a failed probe or groupadd never lets a later command run.
func ensureEach(groups []string, ensure func(string) error) error {
	for _, group := range groups {
		if err := ensure(group); err != nil {
			return err
		}
	}
	return nil
}

// accountPlatform is the OS-specific half of a backend: its declared
// capabilities, how to look an account up, and how to converge an existing
// or a missing one. ensure and converge supply the shared control flow and
// the capability enforcement around it.
type accountPlatform interface {
	// Capabilities declares what the platform honours and refuses.
	Capabilities() Capabilities
	// lookupUser returns the raw passwd-format record for name and whether
	// the account exists.
	lookupUser(name string) (record string, exists bool, err error)
	// ensureExistingUser converges an account that lookupUser found; record
	// is the record it returned before any mutation.
	ensureExistingUser(record string, want DesiredUser) error
	// ensureMissingUser creates the account and any missing groups.
	ensureMissingUser(want DesiredUser) error
}

// converge probes want's account and dispatches to the existing- or
// missing-account path of p. A missing account is created only after the
// declared creation refusals pass. ensure validates want first.
func converge(p accountPlatform, want DesiredUser) error {
	record, exists, err := p.lookupUser(want.Name)
	if err != nil {
		return err
	}
	if exists {
		return p.ensureExistingUser(record, want)
	}
	if err := p.Capabilities().checkCreate(want); err != nil {
		return err
	}
	return p.ensureMissingUser(want)
}

func commandError(command string, args []string, code int, stdout, stderr string) error {
	return fmt.Errorf("%s %s failed (exit %d): %s%s", command, strings.Join(args, " "), code, stdout, stderr)
}
