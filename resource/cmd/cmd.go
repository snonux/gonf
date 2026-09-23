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
	"github.com/snonux/gonf/internal/runners"
	"github.com/snonux/gonf/resource"
	"github.com/snonux/gonf/resource/embed"
	opt "github.com/snonux/gonf/resource/options"
)

var (
	_ opt.Named           = (*Cmd)(nil)
	_ opt.Dirable         = (*Cmd)(nil)
	_ opt.Envable         = (*Cmd)(nil)
	_ opt.Creatable       = (*Cmd)(nil)
	_ opt.Guardable       = (*Cmd)(nil)
	_ opt.Dependable      = (*Cmd)(nil)
	_ opt.Elevatable      = (*Cmd)(nil)
	_ opt.ChangeWatchable = (*Cmd)(nil)
	_ opt.Sensitivable    = (*Cmd)(nil)
	_ opt.MisuseReporter  = (*Cmd)(nil)
)

// Cmd is a command resource. It embeds DependsOn but not Absence: there is no
// meaningful "absent" state for a one-shot command. The ChangeGate embed backs
// the OnChange option: a gated command is skipped unless a watched resource
// changed during this apply.
//
// The Sensitivity embed backs WithSensitive: a sensitive command's argv or
// environment holds secret material, so run withholds the argv from logs
// and the dry-run description and the output from a failure. Plan apply
// sets it from a sensitive command op (plan.Op.Sensitive, whether the scan
// detected the secret or the recipe passed WithSensitive). The ID is still
// logged: recording refuses a strong secret in it (an unnamed command's ID
// is its argv), not a short one, and Present refuses WithSensitive without
// WithName (checkSensitiveName).
type Cmd struct {
	embed.DependsOn
	embed.ChangeGate
	embed.Sensitivity
	embed.Misuse
	name    string // registry name; defaults to "name args..."
	bin     string
	args    []string
	dir     string
	env     map[string]string
	creates string
	unless  *opt.Guard
	onlyIf  *opt.Guard
	elevate bool
	// runFn and probeFn are the injected overrides of runWith/runProbe (see
	// newCmdWith): nil in every real Cmd (newCmd), so run() and
	// guardPasses() fall back to the real internal/exec runner. Set by the
	// command plan.Handler from its ApplyContext.Runners (ctx.Runners.
	// Command, task qb2) and directly by this package's own tests, instead
	// of a process-global internal/testseam fake.
	runFn   func(exec.Opts, string, ...string) (string, string, int, error)
	probeFn func(string, ...string) (string, string, int, error)
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
// WithSensitive requires WithName (checkSensitiveName). A violation, like an
// option misuse, is reported as a declaration error (resource.Refuse) and
// nothing is registered; the refused value carries the command's WithName, or
// only its binary for an unnamed one, so no declared-secret argv reaches it.
func Present(bin string, args []string, opts ...opt.CommandOption) resource.Resource {
	c := newCmd(bin, args, opts)
	err := c.MisuseErr()
	if err == nil {
		err = c.checkSensitiveName()
	}
	if err != nil {
		name := c.name
		if name == "" {
			name = bin
		}
		return resource.Refuse("Command", name, err)
	}
	if c.name == "" {
		c.name = defaultName(bin, c.args)
	}

	r := resource.Register("Command", c.name, c, c.DependsOn.IDs...)
	resource.RecordPlanDraft(c.planDraft(r.ID()))
	return r
}

// Ensure builds and applies a command resource without registering it or
// recording a plan draft.
func Ensure(bin string, args []string, opts ...opt.CommandOption) error {
	return ensureWith(nil, bin, args, opts)
}

// ensureWith is Ensure with cr's runners (nil: the real ones) — the plan
// handler's apply-time entry (task qb2), built with the ApplyContext.Runners.
// Command override for this run, instead of Ensure growing a public runners
// parameter of its own.
func ensureWith(cr *runners.CommandRunners, bin string, args []string, opts []opt.CommandOption) error {
	c := newCmdWith(cr, bin, args, opts)
	if err := c.MisuseErr(); err != nil {
		return err
	}
	if c.name == "" {
		c.name = defaultName(bin, c.args)
	}
	return c.apply()
}

// newCmd builds a Cmd running bin with a copy of args and applies opts,
// using the real runners. An option misuse is left in its embed.Misuse for
// the caller to check.
func newCmd(bin string, args []string, opts []opt.CommandOption) *Cmd {
	return newCmdWith(nil, bin, args, opts)
}

// newCmdWith is newCmd with cr's runners injected (nil: the real ones, via
// runWith/runProbe): the command plan.Handler's apply-time constructor
// (task qb2, mirroring resource/user's newUserWith from task 372) and this
// package's own tests use it directly instead of a package-global fake.
func newCmdWith(cr *runners.CommandRunners, bin string, args []string, opts []opt.CommandOption) *Cmd {
	c := &Cmd{
		bin:  bin,
		args: append([]string(nil), args...),
	}
	if cr != nil {
		c.runFn = cr.Run
		c.probeFn = cr.Probe
	}
	for _, o := range opts {
		o.Apply(c)
	}
	return c
}

// checkSensitiveName refuses an explicitly sensitive command (WithSensitive)
// without WithName: an unnamed command's ID is "bin args...", and IDs are
// logged and reported unredacted on every host (apply logs, the changed
// summary, SensitiveOpNames in the -stdout refusal and the plan -o
// warning), so the argv the recipe declared secret would leak through the
// ID. The message names the binary only. Ensure does not check: the plan
// handler rebuilds a scan-marked unnamed command through it, whose argv
// recording already vetted (a strong secret in an ID is refused there).
func (c *Cmd) checkSensitiveName() error {
	if c.Sensitive && c.name == "" {
		return fmt.Errorf("command %s: WithSensitive requires WithName: an unnamed command's ID is its argv, "+
			"which is logged and reported on every host", c.bin)
	}
	return nil
}

// planDraft records c as a "command" plan draft under id, including its
// guards, dependencies and OnChange gate. Command's exclusive fields travel
// in Payload (see Payload, task w62 Layer 1); Env stays flat since package
// drafts reuse it too.
func (c *Cmd) planDraft(id string) resource.PlanDraft {
	d := resource.PlanDraft{
		Kind: "command",
		ID:   id,
		Name: c.name,
		Payload: Payload{
			Bin:     c.bin,
			Args:    append([]string(nil), c.args...),
			Dir:     c.dir,
			Creates: c.creates,
			Unless:  planGuardDraft(c.unless),
			OnlyIf:  planGuardDraft(c.onlyIf),
		},
		Deps: c.DependsOn.SortedIDs(),
	}
	// The draft gets its own copy: stored drafts outlive this Cmd and must
	// not share mutable state with it. maps.Clone keeps nil as nil.
	d.Env = maps.Clone(c.env)
	d.Elevate = c.elevate
	d.Sensitive = c.Sensitive
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
		ok, err := c.guardPasses(c.unless)
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
		ok, err := c.guardPasses(c.onlyIf)
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
// logs it), failing on a non-zero exit with its stdout and stderr — for a
// sensitive command only their sizes, since a program may echo its argv or
// its secret input.
func (c *Cmd) run() error {
	desc := fmt.Sprintf("run %s", c.commandLine())
	return resource.Mutate(c.id(), desc, func() error {
		opts := exec.Opts{Dir: c.dir}
		if c.env != nil {
			opts.Env = exec.MergeEnv(c.env)
		}

		logger.Info("running %s: %s", c.id(), c.commandLine())
		stdout, stderr, exitCode, err := c.runWith(opts, c.bin, c.args...)
		if err != nil {
			return fmt.Errorf("failed to execute %s: %w", c.bin, err)
		}
		if exitCode != 0 && c.Sensitive {
			return fmt.Errorf("%s exited %d (output withheld: %d bytes stdout, %d bytes stderr; the command carries secret material)",
				c.bin, exitCode, len(stdout), len(stderr))
		}
		if exitCode != 0 {
			return fmt.Errorf("%s exited %d\nstdout: %s\nstderr: %s",
				c.bin, exitCode, stdout, stderr)
		}
		if stdout != "" && !c.Sensitive {
			logger.Debug("%s stdout: %s", c.id(), strings.TrimSpace(stdout))
		}
		return nil
	})
}

// commandLine is the command as logs and descriptions show it: bin and
// argv, or for a sensitive command bin only, with the argv withheld.
func (c *Cmd) commandLine() string {
	if c.Sensitive {
		return c.bin + " [argv withheld: secret material]"
	}
	if len(c.args) == 0 {
		return c.bin + " "
	}
	return c.bin + " " + strings.Join(c.args, " ")
}

// guardPasses runs guard probe g through c's runner (c.probeFn when
// injected, else the real one) and reports whether it exited with the
// expected code and, when set, printed the expected trimmed stdout.
func (c *Cmd) guardPasses(g *opt.Guard) (bool, error) {
	stdout, _, exitCode, err := c.runProbe(g.Name, g.Args...)
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

// runWith runs the main command (it carries Dir/Env opts): c.runFn when a
// runners.CommandRunners was injected (newCmdWith), otherwise the real
// internal/exec runner.
func (c *Cmd) runWith(opts exec.Opts, name string, args ...string) (string, string, int, error) {
	if c.runFn != nil {
		return c.runFn(opts, name, args...)
	}
	return exec.RunWith(opts, name, args...)
}

// runProbe runs an Unless/OnlyIf guard probe: c.probeFn when injected,
// otherwise the real internal/exec runner.
func (c *Cmd) runProbe(name string, args ...string) (string, string, int, error) {
	if c.probeFn != nil {
		return c.probeFn(name, args...)
	}
	return exec.Run(name, args...)
}
