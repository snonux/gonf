// Package cmd implements the command resource: run an argv with optional
// idempotency guards (Unless, OnlyIf, Creates).
package cmd

import (
	"fmt"
	"os"
	"strings"

	opt "github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/internal/exec"
	"github.com/snonux/gonf/internal/logger"
	"github.com/snonux/gonf/resource"
	"github.com/snonux/gonf/resource/embed"
)

// Cmd is a command resource. It embeds DependsOn but not Absence: there is no
// meaningful "absent" state for a one-shot command.
type Cmd struct {
	embed.DependsOn
	name    string // registry name; defaults to "name args..."
	bin     string
	args    []string
	dir     string
	env     map[string]string
	creates string
	unless  *opt.Guard
	onlyIf  *opt.Guard
}

func (c *Cmd) SetName(name string)          { c.name = name }
func (c *Cmd) SetDir(dir string)            { c.dir = dir }
func (c *Cmd) SetEnv(env map[string]string) { c.env = env }
func (c *Cmd) SetCreates(path string)       { c.creates = path }
func (c *Cmd) SetUnless(g *opt.Guard)       { c.unless = g }
func (c *Cmd) SetOnlyIf(g *opt.Guard)       { c.onlyIf = g }

var (
	_ opt.Named      = (*Cmd)(nil)
	_ opt.Dirable    = (*Cmd)(nil)
	_ opt.Envable    = (*Cmd)(nil)
	_ opt.Creatable  = (*Cmd)(nil)
	_ opt.Guardable  = (*Cmd)(nil)
	_ opt.Dependable = (*Cmd)(nil)
)

// Present registers a command resource that runs bin with args on Apply.
func Present(bin string, args []string, opts ...opt.Option) resource.Resource {
	c := &Cmd{
		bin:  bin,
		args: append([]string(nil), args...),
	}
	for _, o := range opts {
		o(c)
	}
	if c.name == "" {
		c.name = defaultName(bin, c.args)
	}

	r := resource.Register("Command", c.name,
		resource.ApplierFunc(func() error { return c.apply() }), c.DependsOn.IDs...)
	resource.RecordPlanDraft(c.planDraft(r.ID()))
	return r
}

// Ensure builds and applies a command resource without registering it or
// recording a plan draft.
func Ensure(bin string, args []string, opts ...opt.Option) error {
	c := &Cmd{
		bin:  bin,
		args: append([]string(nil), args...),
	}
	for _, o := range opts {
		o(c)
	}
	if c.name == "" {
		c.name = defaultName(bin, c.args)
	}
	return c.apply()
}

func (c *Cmd) planDraft(id string) resource.PlanDraft {
	d := resource.PlanDraft{
		Kind:    "command",
		ID:      id,
		Name:    c.name,
		Bin:     c.bin,
		Args:    append([]string(nil), c.args...),
		Dir:     c.dir,
		Creates: c.creates,
	}
	if c.env != nil {
		d.Env = make(map[string]string, len(c.env))
		for k, v := range c.env {
			d.Env[k] = v
		}
	}
	d.Unless = planGuardDraft(c.unless)
	d.OnlyIf = planGuardDraft(c.onlyIf)
	return d
}

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

func defaultName(bin string, args []string) string {
	if len(args) == 0 {
		return bin
	}
	return bin + " " + strings.Join(args, " ")
}

func (c *Cmd) apply() error {
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

func (c *Cmd) id() string {
	return fmt.Sprintf("Command[%s]", c.name)
}

func (c *Cmd) run() error {
	if resource.DryRun() {
		resource.Note(c.id(), resource.StatusWouldChange)
		logger.Info("dry-run: would run %s %s", c.bin, strings.Join(c.args, " "))
		return nil
	}

	opts := exec.Opts{Dir: c.dir}
	if c.env != nil {
		opts.Env = exec.MergeEnv(c.env)
	}

	logger.Info("running %s: %s %s", c.id(), c.bin, strings.Join(c.args, " "))
	stdout, stderr, exitCode, err := exec.RunWith(opts, c.bin, c.args...)
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
	resource.Note(c.id(), resource.StatusChanged)
	return nil
}

func guardPasses(g *opt.Guard) (bool, error) {
	stdout, _, exitCode, err := exec.Run(g.Name, g.Args...)
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
