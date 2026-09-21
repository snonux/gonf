// Package cron implements a Puppet-inspired cron job resource managed via
// per-user crontab entries (Linux, FreeBSD, NetBSD, OpenBSD).
package cron

import (
	"fmt"
	"slices"
	"strings"

	"github.com/snonux/gonf/internal/exec"
	"github.com/snonux/gonf/internal/logger"
	"github.com/snonux/gonf/resource"
	"github.com/snonux/gonf/resource/embed"
	opt "github.com/snonux/gonf/resource/options"
)

// cronType is the Cron resource's type label.
const cronType = "Cron"

// beginMarkerPrefix and endMarkerPrefix start the lines that delimit each
// Gonf-managed job block in a crontab; the job name and "]" follow.
const (
	beginMarkerPrefix = "# BEGIN GONF Cron["
	endMarkerPrefix   = "# END GONF Cron["
)

var (
	// Register takes the value as a resource.Applier; asserting it here reports a
	// renamed or re-signed Apply at the declaration, not at the Register call.
	_ resource.Applier          = (*Cron)(nil)
	_ opt.Absentable            = (*Cron)(nil)
	_ opt.Dependable            = (*Cron)(nil)
	_ opt.CronUserable          = (*Cron)(nil)
	_ opt.LegacyCronCommandable = (*Cron)(nil)
	_ opt.Commandable           = (*Cron)(nil)
	_ opt.Minuteable            = (*Cron)(nil)
	_ opt.Hourable              = (*Cron)(nil)
	_ opt.Monthdayable          = (*Cron)(nil)
	_ opt.Monthable             = (*Cron)(nil)
	_ opt.Weekdayable           = (*Cron)(nil)
	_ opt.CronEnvable           = (*Cron)(nil)
)

// runCmd reads a crontab (crontab -l) and runCmdWithStdin writes one (crontab
// -, fed via stdin); both are swapped in unit tests. acquireCrontabLock takes
// the write lock and is swapped together with them (see SetRunnersForTest).
var (
	runCmd             = exec.Run
	runCmdWithStdin    = exec.RunWithStdin
	acquireCrontabLock = lockCrontab
)

// Cron manages a named crontab entry for a user (default root).
type Cron struct {
	embed.DependsOn
	embed.Absence
	name     string
	user     string
	legacy   string
	command  string
	minute   string
	hour     string
	monthday string
	month    string
	weekday  string
	env      []string
}

// newCron builds a Cron with defaults applied, then applies opts.
func newCron(name string, opts []opt.CronOption) *Cron {
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
		o.Apply(c)
	}
	return c
}

// SetCronUser sets the account whose crontab is managed (default root).
func (c *Cron) SetCronUser(u string) { c.user = u }

// SetLegacyCommand opts into adopting one unmanaged crontab line whose parsed
// command is exactly cmd (see opt.WithLegacyCommand).
func (c *Cron) SetLegacyCommand(cmd string) { c.legacy = cmd }

// SetCommand sets the command the cron job runs.
func (c *Cron) SetCommand(cmd string) { c.command = cmd }

// SetMinute sets the minute schedule field (default "*").
func (c *Cron) SetMinute(v string) { c.minute = v }

// SetHour sets the hour schedule field (default "*").
func (c *Cron) SetHour(v string) { c.hour = v }

// SetMonthday sets the day-of-month schedule field (default "*").
func (c *Cron) SetMonthday(v string) { c.monthday = v }

// SetMonth sets the month schedule field (default "*").
func (c *Cron) SetMonth(v string) { c.month = v }

// SetWeekday sets the day-of-week schedule field (default "*").
func (c *Cron) SetWeekday(v string) { c.weekday = v }

// AddCronEnv appends a KEY=VAL environment line above the cron job.
func (c *Cron) AddCronEnv(kv string) { c.env = append(c.env, kv) }

// Present registers a cron job that should exist in the user's crontab.
func Present(name string, opts ...opt.CronOption) resource.Resource {
	c := newCron(name, opts)
	r := resource.Register(cronType, c.regName(), c, c.DependsOn.IDs...)
	resource.RecordPlanDraft(c.planDraft(r.ID()))
	return r
}

// Ensure applies a cron job without registering it or recording a plan draft.
func Ensure(name string, opts ...opt.CronOption) error {
	return newCron(name, opts).apply()
}

// Absent removes a named cron job from the user's crontab.
func Absent(name string, opts ...opt.CronOption) resource.Resource {
	opts = append(slices.Clone(opts), opt.IsAbsent)
	return Present(name, opts...)
}

// SetRunnersForTest swaps the crontab command runners (tests only). A nil
// argument keeps the current runner for that slot. It also replaces the
// cross-process crontab lock with an in-process one: a faked crontab is not
// shared with other processes, and the real lock would create state in the
// test user's home directory (lock.go) from every package that fakes cron.
func SetRunnersForTest(run func(name string, args ...string) (string, string, int, error), runWithStdin func(stdin string, name string, args ...string) (string, string, int, error)) {
	if run != nil {
		runCmd = run
	}
	if runWithStdin != nil {
		runCmdWithStdin = runWithStdin
	}
	acquireCrontabLock = lockCrontabInProcess
}

// ResetRunnersForTest restores the real crontab command runners and lock.
func ResetRunnersForTest() {
	runCmd = exec.Run
	runCmdWithStdin = exec.RunWithStdin
	acquireCrontabLock = lockCrontab
}

// Apply runs the cron reconciliation directly for the legacy resource path.
func (c *Cron) Apply() error { return c.apply() }

// planDraft records c as a "cron" plan draft under id.
func (c *Cron) planDraft(id string) resource.PlanDraft {
	return resource.PlanDraft{
		Kind:          "cron",
		ID:            id,
		Name:          c.name,
		CronUser:      c.user,
		Command:       c.command,
		LegacyCommand: c.legacy,
		Schedule: strings.Join([]string{
			c.minute, c.hour, c.monthday, c.month, c.weekday,
		}, " "),
		CronEnv: append([]string(nil), c.env...),
		Absent:  c.Absent,
		Deps:    c.DependsOn.SortedIDs(),
	}
}

// regName is the Cron resource's registered name, "<user>/<name>": one cron
// job name may exist once per crontab.
func (c *Cron) regName() string { return c.user + "/" + c.name }

// id is the Cron resource's ID, the same value Present registers, so apply
// reports under exactly the registered ID.
func (c *Cron) id() string { return resource.FormatID(cronType, c.regName()) }

func (c *Cron) apply() error {
	id := c.id()
	if err := c.validate(); err != nil {
		return fmt.Errorf("%s: %w", id, err)
	}

	// A dry-run still reads and renders the target crontab to report an
	// accurate preview, but acquiring this file lock would create filesystem
	// state. There is no write transaction to serialize in that case.
	if resource.DryRun() {
		return c.reconcile(id)
	}

	// crontab has no compare-and-swap write. Hold a per-crontab advisory lock
	// across the read/merge/write transaction so separate Gonf processes cannot
	// discard each other's changes. lock.go documents where the lock lives and
	// why a non-root apply for another account is refused here.
	unlock, err := acquireCrontabLock(c.user)
	if err != nil {
		return err
	}
	defer func() { _ = unlock() }()

	return c.reconcile(id)
}

func (c *Cron) reconcile(id string) error {
	current, err := readCrontab(c.user)
	if err != nil {
		return err
	}

	desired := ""
	if !c.Absent {
		desired = c.block()
	}

	adopted, adoptedChanged := adoptLegacyCommand(current, c.legacy)
	newTab, changed := mergeCrontab(adopted, c.name, desired)
	changed = changed || adoptedChanged
	if !changed {
		resource.Note(id, resource.StatusOK)
		return nil
	}

	desc := fmt.Sprintf("update crontab for %s (job %s)", c.user, c.name)
	return resource.Mutate(id, desc, func() error {
		if err := writeCrontab(c.user, newTab); err != nil {
			return err
		}
		logger.Info("updated crontab for %s (job %s)", c.user, c.name)
		return nil
	})
}

func (c *Cron) validate() error {
	if c.name == "" {
		return fmt.Errorf("name must not be empty")
	}
	if strings.ContainsAny(c.name, " \t\n\r[]") {
		return fmt.Errorf("name must not contain whitespace or brackets")
	}
	if strings.TrimSpace(c.user) == "" {
		return fmt.Errorf("WithCronUser must not be empty")
	}
	if !c.Absent {
		if strings.TrimSpace(c.command) == "" {
			return fmt.Errorf("WithCommand is required")
		}
		if strings.ContainsAny(c.command, "\n\r") {
			return fmt.Errorf("command must not contain newlines")
		}
	}
	if c.legacy != "" {
		if c.Absent {
			return fmt.Errorf("WithLegacyCommand cannot be used with an absent cron job")
		}
		if strings.TrimSpace(c.legacy) == "" || strings.ContainsAny(c.legacy, "\n\r") {
			return fmt.Errorf("WithLegacyCommand must be a non-empty single-line command")
		}
	}
	for _, field := range []struct {
		label, value string
	}{
		{"minute", c.minute},
		{"hour", c.hour},
		{"monthday", c.monthday},
		{"month", c.month},
		{"weekday", c.weekday},
	} {
		if !validCronField(field.value, cronFieldRange(field.label)) {
			return fmt.Errorf("%s field must use portable cron syntax", field.label)
		}
	}
	for _, e := range c.env {
		e = strings.TrimSpace(e)
		if e == "" || strings.ContainsAny(e, "\n\r") {
			return fmt.Errorf("WithCronEnv values must be non-empty single-line KEY=VAL")
		}
		if !strings.Contains(e, "=") {
			return fmt.Errorf("WithCronEnv values must be KEY=VAL")
		}
		if strings.HasPrefix(e, "# BEGIN GONF") || strings.HasPrefix(e, "# END GONF") {
			return fmt.Errorf("WithCronEnv must not look like a GONF marker")
		}
	}
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

func beginMarker(name string) string { return beginMarkerPrefix + name + "]" }
func endMarker(name string) string   { return endMarkerPrefix + name + "]" }
