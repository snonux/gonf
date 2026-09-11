// Package cron implements a Puppet-inspired cron job resource managed via
// per-user crontab entries (Linux, FreeBSD, NetBSD, OpenBSD).
package cron

import (
	"fmt"
	"strings"

	opt "github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/internal/exec"
	"github.com/snonux/gonf/internal/logger"
	"github.com/snonux/gonf/resource"
	"github.com/snonux/gonf/resource/embed"
)

// Cron manages a named crontab entry for a user (default root).
type Cron struct {
	embed.DependsOn
	embed.Absence
	name     string
	user     string
	command  string
	minute   string
	hour     string
	monthday string
	month    string
	weekday  string
	env      []string
}

func (c *Cron) SetCronUser(u string)  { c.user = u }
func (c *Cron) SetCommand(cmd string) { c.command = cmd }
func (c *Cron) SetMinute(v string)    { c.minute = v }
func (c *Cron) SetHour(v string)      { c.hour = v }
func (c *Cron) SetMonthday(v string)  { c.monthday = v }
func (c *Cron) SetMonth(v string)     { c.month = v }
func (c *Cron) SetWeekday(v string)   { c.weekday = v }
func (c *Cron) AddCronEnv(kv string)  { c.env = append(c.env, kv) }

var (
	_ opt.Absentable   = (*Cron)(nil)
	_ opt.Dependable   = (*Cron)(nil)
	_ opt.CronUserable = (*Cron)(nil)
	_ opt.Commandable  = (*Cron)(nil)
	_ opt.Minuteable   = (*Cron)(nil)
	_ opt.Hourable     = (*Cron)(nil)
	_ opt.Monthdayable = (*Cron)(nil)
	_ opt.Monthable    = (*Cron)(nil)
	_ opt.Weekdayable  = (*Cron)(nil)
	_ opt.CronEnvable  = (*Cron)(nil)
)

// runCmd is swapped in tests.
var runCmd = exec.Run

// Present registers a cron job that should exist in the user's crontab.
func Present(name string, opts ...opt.Option) resource.Resource {
	c := &Cron{
		name:     name,
		user:     "root",
		minute:   "*",
		hour:     "*",
		monthday: "*",
		month:    "*",
		weekday:  "*",
	}
	for _, o := range opts {
		o(c)
	}
	regName := c.user + "/" + c.name
	return resource.Register("Cron", regName,
		resource.ApplierFunc(func() error { return c.apply() }), c.DependsOn.IDs...)
}

// Absent removes a named cron job from the user's crontab.
func Absent(name string, opts ...opt.Option) resource.Resource {
	opts = append(opts, opt.IsAbsent)
	return Present(name, opts...)
}

func (c *Cron) apply() error {
	id := fmt.Sprintf("Cron[%s/%s]", c.user, c.name)
	if !c.Absent && c.command == "" {
		return fmt.Errorf("%s: WithCommand is required", id)
	}
	if strings.ContainsAny(c.name, " \t\n") {
		return fmt.Errorf("%s: name must not contain whitespace", id)
	}

	current, err := readCrontab(c.user)
	if err != nil {
		return err
	}

	desired := ""
	if !c.Absent {
		desired = c.block()
	}

	newTab, changed := mergeCrontab(current, c.name, desired)
	if !changed {
		resource.Note(id, resource.StatusOK)
		return nil
	}

	if resource.DryRun() {
		logger.Info("dry-run: would update crontab for %s (job %s)", c.user, c.name)
		resource.Note(id, resource.StatusWouldChange)
		return nil
	}

	if err := writeCrontab(c.user, newTab); err != nil {
		return err
	}
	logger.Info("updated crontab for %s (job %s)", c.user, c.name)
	resource.Note(id, resource.StatusChanged)
	return nil
}

func (c *Cron) block() string {
	var b strings.Builder
	b.WriteString(beginMarker(c.name))
	b.WriteByte('\n')
	for _, e := range c.env {
		b.WriteString(e)
		b.WriteByte('\n')
	}
	fmt.Fprintf(&b, "%s %s %s %s %s %s\n",
		c.minute, c.hour, c.monthday, c.month, c.weekday, c.command)
	b.WriteString(endMarker(c.name))
	b.WriteByte('\n')
	return b.String()
}

func beginMarker(name string) string { return "# BEGIN GONF Cron[" + name + "]" }
func endMarker(name string) string   { return "# END GONF Cron[" + name + "]" }
