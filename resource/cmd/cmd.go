// Package cmd implements the command resource: run an argv with optional
// idempotency guards (Unless, OnlyIf, Creates).
package cmd

import (
	"fmt"
	"maps"
	"os"
	"strings"

	"github.com/snonux/gonf/internal/exec"
	"github.com/snonux/gonf/internal/logger"
	"github.com/snonux/gonf/resource"
	"github.com/snonux/gonf/resource/embed"
	opt "github.com/snonux/gonf/resource/options"
)

// runWith runs the main command (it carries Dir/Env opts) and runProbe runs
// guard probes (Unless/OnlyIf). Both are swapped in unit tests.
var (
	runWith  = exec.RunWith
	runProbe = exec.Run
)

var (
	// Register takes the value as a resource.Applier; asserting it here reports a
	// renamed or re-signed Apply at the declaration, not at the Register call.
	_ resource.Applier    = (*Cmd)(nil)
	_ opt.Named           = (*Cmd)(nil)
	_ opt.Dirable         = (*Cmd)(nil)
	_ opt.Envable         = (*Cmd)(nil)
	_ opt.Creatable       = (*Cmd)(nil)
	_ opt.Guardable       = (*Cmd)(nil)
	_ opt.Dependable      = (*Cmd)(nil)
	_ opt.Elevatable      = (*Cmd)(nil)
	_ opt.ChangeWatchable = (*Cmd)(nil)
)

// Cmd is a command resource. It embeds DependsOn but not Absence: there is no
// meaningful "absent" state for a one-shot command. The ChangeGate embed backs
// the OnChange option: a gated command is skipped unless a watched resource
// changed during this apply.
type Cmd struct {
	embed.DependsOn
	embed.ChangeGate
	name    string // registry name; defaults to "name args..."
	bin     string
	args    []string
	dir     string
	env     map[string]string
	creates string
	unless  *opt.Guard
	onlyIf  *opt.Guard
	elevate bool
}

// SetName overrides the registry name, which otherwise defaults to the
// command line ("bin args...").
func (c *Cmd) SetName(name string) { c.name = name }

// SetDir sets the working directory of the main command.
func (c *Cmd) SetDir(dir string) { c.dir = dir }

// SetCreates skips the command when path already exists.
func (c *Cmd) SetCreates(path string) { c.creates = path }

// SetUnless skips the command when guard g succeeds.
func (c *Cmd) SetUnless(g *opt.Guard) { c.unless = g }

// SetOnlyIf runs the command only when guard g succeeds.
func (c *Cmd) SetOnlyIf(g *opt.Guard) { c.onlyIf = g }

// SetElevate marks the command for privileged execution. The flag is carried
// on the plan draft; the plan engine decides how to elevate.
func (c *Cmd) SetElevate() { c.elevate = true }

// SetEnv configures extra environment variables for the main command. It
// copies the caller's map (nil stays nil) so a recipe that mutates or reuses
// the map after WithEnv cannot alter the registered resource, its plan draft
// or its recorded plan op. Package.SetEnv follows the same contract, since
// both are reached through the one shared WithEnv option.
func (c *Cmd) SetEnv(env map[string]string) { c.env = maps.Clone(env) }

// Present registers a command resource that runs bin with args on Apply.
func Present(bin string, args []string, opts ...opt.CommandOption) resource.Resource {
	c, err := newCmd(bin, args, opts)
	if err != nil {
		logger.Fatal("%v", err)
	}
	r := resource.Register("Command", c.name, c, c.DependsOn.IDs...)
	resource.RecordPlanDraft(c.planDraft(r.ID()))
	return r
}

// Ensure builds and applies a command resource without registering it or
// recording a plan draft.
func Ensure(bin string, args []string, opts ...opt.CommandOption) error {
	c, err := newCmd(bin, args, opts)
	if err != nil {
		return err
	}
	return c.apply()
}

// newCmd builds a Cmd running bin with a copy of args, applies opts and
// defaults the registry name. The error reports a change gate armed with
// nothing to watch (the legacy IfChanged reaching a Command through the
// type-erased Option path): it could never fire, so Present aborts and
// Ensure fails instead of skipping the command forever.
func newCmd(bin string, args []string, opts []opt.CommandOption) (*Cmd, error) {
	c := &Cmd{
		bin:  bin,
		args: append([]string(nil), args...),
	}
	for _, o := range opts {
		o.Apply(c)
	}
	if c.name == "" {
		c.name = defaultName(bin, c.args)
	}
	if err := c.CheckWatch(); err != nil {
		return c, fmt.Errorf("%s: %w", c.id(), err)
	}
	return c, nil
}

// SetRunnersForTest swaps the command runners (tests only). A nil argument
// keeps the current runner for that slot.
func SetRunnersForTest(run func(opts exec.Opts, name string, args ...string) (string, string, int, error), probe func(name string, args ...string) (string, string, int, error)) {
	if run != nil {
		runWith = run
	}
	if probe != nil {
		runProbe = probe
	}
}

// ResetRunnersForTest restores the real command runners.
func ResetRunnersForTest() {
	runWith = exec.RunWith
	runProbe = exec.Run
}

// Apply runs the command directly for the legacy resource path.
func (c *Cmd) Apply() error { return c.apply() }

// planDraft records c as a "command" plan draft under id, including its
// guards, dependencies and OnChange gate.
func (c *Cmd) planDraft(id string) resource.PlanDraft {
	d := resource.PlanDraft{
		Kind:    "command",
		ID:      id,
		Name:    c.name,
		Bin:     c.bin,
		Args:    append([]string(nil), c.args...),
		Dir:     c.dir,
		Creates: c.creates,
		Deps:    c.DependsOn.SortedIDs(),
	}
	// The draft gets its own copy: stored drafts outlive this Cmd and must
	// not share mutable state with it. maps.Clone keeps nil as nil.
	d.Env = maps.Clone(c.env)
	d.Unless = planGuardDraft(c.unless)
	d.OnlyIf = planGuardDraft(c.onlyIf)
	d.Elevate = c.elevate
	d.IfChanged, d.Watch = c.DraftGate()
	return d
}

// planGuardDraft converts guard g to its plan-draft form (nil stays nil). The
// default expected exit code 0 is left unset on the draft.
func planGuardDraft(g *opt.Guard) *resource.PlanGuardDraft {
	if g == nil {
		return nil
	}
	out := &resource.PlanGuardDraft{
		Bin:          g.Name,
		Args:         append([]string(nil), g.Args...),
		ExpectStdout: g.ExpectStdout,
	}
	if g.ExpectExit != 0 {
		e := g.ExpectExit
		out.ExpectExit = &e
	}
	return out
}

// defaultName is the registry name of an unnamed command: bin followed by
// its space-joined args.
func defaultName(bin string, args []string) string {
	if len(args) == 0 {
		return bin
	}
	return bin + " " + strings.Join(args, " ")
}

// apply checks, in order, the OnChange gate and the Creates, Unless and OnlyIf
// guards; the first one that says to skip notes the command skipped.
// Otherwise it runs the command.
func (c *Cmd) apply() error {
	// The change gate (OnChange) is checked first: a held command is skipped
	// entirely, before any Creates/Unless/OnlyIf probe runs.
	if c.Holds(resource.AnyChanged) {
		logger.Info("skipping %s: no watched dependency changed", c.id())
		resource.Note(c.id(), resource.StatusSkipped)
		return nil
	}

	if c.creates != "" {
		if _, err := os.Stat(c.creates); err == nil {
			logger.Info("skipping %s: %s already exists", c.id(), c.creates)
			resource.Note(c.id(), resource.StatusSkipped)
			return nil
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("creates check for %s: %w", c.creates, err)
		}
	}

	if c.unless != nil {
		ok, err := guardPasses(c.unless)
		if err != nil {
			return fmt.Errorf("unless guard for %s: %w", c.id(), err)
		}
		if ok {
			logger.Info("skipping %s: unless guard succeeded", c.id())
			resource.Note(c.id(), resource.StatusSkipped)
			return nil
		}
	}

	if c.onlyIf != nil {
		ok, err := guardPasses(c.onlyIf)
		if err != nil {
			return fmt.Errorf("onlyIf guard for %s: %w", c.id(), err)
		}
		if !ok {
			logger.Info("skipping %s: onlyIf guard did not succeed", c.id())
			resource.Note(c.id(), resource.StatusSkipped)
			return nil
		}
	}

	return c.run()
}

// id is the resource ID the command registers and reports under.
func (c *Cmd) id() string {
	return resource.FormatID("Command", c.name)
}

// run executes the main command through resource.Mutate (so dry-run only
// logs it), failing on a non-zero exit with its stdout and stderr.
func (c *Cmd) run() error {
	desc := fmt.Sprintf("run %s %s", c.bin, strings.Join(c.args, " "))
	return resource.Mutate(c.id(), desc, func() error {
		opts := exec.Opts{Dir: c.dir}
		if c.env != nil {
			opts.Env = exec.MergeEnv(c.env)
		}

		logger.Info("running %s: %s %s", c.id(), c.bin, strings.Join(c.args, " "))
		stdout, stderr, exitCode, err := runWith(opts, c.bin, c.args...)
		if err != nil {
			return fmt.Errorf("failed to execute %s: %w", c.bin, err)
		}
		if exitCode != 0 {
			return fmt.Errorf("%s exited %d\nstdout: %s\nstderr: %s",
				c.bin, exitCode, stdout, stderr)
		}
		if stdout != "" {
			logger.Debug("%s stdout: %s", c.id(), strings.TrimSpace(stdout))
		}
		return nil
	})
}

// guardPasses runs guard probe g and reports whether it exited with the
// expected code and, when set, printed the expected trimmed stdout.
func guardPasses(g *opt.Guard) (bool, error) {
	stdout, _, exitCode, err := runProbe(g.Name, g.Args...)
	if err != nil {
		return false, err
	}
	if exitCode != g.ExpectExit {
		return false, nil
	}
	if g.ExpectStdout != "" && strings.TrimSpace(stdout) != g.ExpectStdout {
		return false, nil
	}
	return true, nil
}
