// Package cron implements a Puppet-inspired cron job resource managed via
// per-user crontab entries (Linux, FreeBSD, NetBSD, OpenBSD).
package cron

import (
	"fmt"
	"slices"
	"strings"

	"github.com/snonux/gonf/internal/logger"
	"github.com/snonux/gonf/internal/runners"
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
	_ opt.Scheduleable          = (*Cron)(nil)
	_ opt.Sensitivable          = (*Cron)(nil)
	_ opt.MisuseReporter        = (*Cron)(nil)
)

// Cron manages a named crontab entry for a user (default root).
//
// The Sensitivity embed backs WithSensitive: the job's command or
// environment lines hold secret material, so its recorded op is sensitive.
// Apply needs no flag of its own: a failing crontab run reports only the
// sizes of its output for every job (crontabFailure), since the one table
// holds every job's lines. Plan apply still sets it from a sensitive cron
// op, like every kind that accepts WithSensitive.
type Cron struct {
	embed.DependsOn
	embed.Absence
	embed.Sensitivity
	embed.Misuse
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
	// readFn and writeFn are the injected overrides of the crontab -l and
	// crontab - runners (see runCrontabRead/runCrontabWrite; nil: the real
	// internal/exec ones), set by newCronWith from a *runners.CronRunners
	// (task fg2, replacing internal/testseam's process-global FakeCrontab).
	readFn  func(string, ...string) (string, string, int, error)
	writeFn func(string, string, ...string) (string, string, int, error)
	// inProcessLock selects lockCrontabInProcess over the real cross-process
	// lock (acquireCrontabLock): set while the crontab is faked, unless the
	// injected runners ask for the real lock (runners.CronRunners doc).
	inProcessLock bool
}

// newCron builds a Cron with defaults applied, then applies opts, using the
// real crontab runners and lock. An option misuse is left in its
// embed.Misuse for the caller to check.
func newCron(name string, opts []opt.CronOption) *Cron {
	return newCronWith(nil, name, opts)
}

// newCronWith is newCron with cr's runners injected (nil: the real ones):
// the constructor EnsureWith, and so the cron plan.Handler, builds with
// (task fg2, mirroring resource/cmd's newCmdWith).
func newCronWith(cr *runners.CronRunners, name string, opts []opt.CronOption) *Cron {
	c := &Cron{
		name:     name,
		user:     "root",
		minute:   "*",
		hour:     "*",
		monthday: "*",
		month:    "*",
		weekday:  "*",
	}
	if cr != nil {
		c.readFn = cr.Read
		c.writeFn = cr.Write
		c.inProcessLock = !cr.CrossProcessLock
	}
	for _, o := range opts {
		o.Apply(c)
	}
	return c
}

// SetCronUser sets the account whose crontab is managed (default root).
func (c *Cron) SetCronUser(u string) { c.user = u }

// SetLegacyCommand opts into adopting the unmanaged crontab lines whose
// parsed command is exactly cmd, on any schedule (see opt.WithLegacyCommand
// and adoption). An identical line is adopted without it.
func (c *Cron) SetLegacyCommand(cmd string) { c.legacy = cmd }

// SetSchedule sets all five schedule fields from one crontab-style string
// ("10 6 * * *", see opt.WithSchedule). A schedule that is not five portable
// fields is option misuse, reported to the job's embed.Misuse, and leaves
// the fields unchanged. A later per-field option still overrides its field.
func (c *Cron) SetSchedule(schedule string) {
	fields, err := splitSchedule(schedule)
	if err != nil {
		c.ReportMisuse(fmt.Errorf("WithSchedule: %w", err))
		return
	}
	c.minute, c.hour, c.monthday, c.month, c.weekday = fields[0], fields[1], fields[2], fields[3], fields[4]
}

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

// Present registers a cron job that should exist in the user's crontab. An
// option misuse is reported as a declaration error (resource.Refuse) and
// nothing is registered.
func Present(name string, opts ...opt.CronOption) resource.Resource {
	c := newCron(name, opts)
	if err := c.MisuseErr(); err != nil {
		return resource.Refuse(cronType, c.regName(), err)
	}
	r, ok := resource.Register(cronType, c.regName(), c, c.DependsOn.IDs...)
	if ok {
		resource.RecordPlanDraft(c.planDraft(r.ID()))
	}
	return r
}

// Ensure applies a cron job without registering it or recording a plan draft.
// An option misuse is returned instead of applied around.
func Ensure(name string, opts ...opt.CronOption) error {
	return EnsureWith(nil, name, opts...)
}

// EnsureWith is Ensure with cr's crontab runners (nil: the real ones). The
// plan handler applies through it with this apply's
// plan.ApplyContext.Runners.Cron (task fg2). It is exported, unlike
// resource/cmd's ensureWith, for the same reason as timer.EnsureWith: a
// cross-package test (api's option-fitness test) compares a direct apply
// against a plan round trip with a faked crontab. Only this module can
// build a *runners.CronRunners, so an external caller can pass nil only.
func EnsureWith(cr *runners.CronRunners, name string, opts ...opt.CronOption) error {
	c := newCronWith(cr, name, opts)
	if err := c.MisuseErr(); err != nil {
		return err
	}
	return c.apply()
}

// Absent removes a named cron job from the user's crontab.
func Absent(name string, opts ...opt.CronOption) resource.Resource {
	opts = append(slices.Clone(opts), opt.IsAbsent)
	return Present(name, opts...)
}

// planDraft records c as a "cron" plan draft under id. Its cron-exclusive
// fields travel in Payload (see resource/cron.Payload, task w62 Layer 1);
// Command stays a flat resource.PlanDraft field because systemd_timer
// reuses it too.
func (c *Cron) planDraft(id string) resource.PlanDraft {
	return resource.PlanDraft{
		Kind:    "cron",
		ID:      id,
		Name:    c.name,
		Command: c.command,
		Payload: Payload{
			CronUser:      c.user,
			LegacyCommand: c.legacy,
			Schedule: strings.Join([]string{
				c.minute, c.hour, c.monthday, c.month, c.weekday,
			}, " "),
			CronEnv: append([]string(nil), c.env...),
		},
		Absent:    c.Absent,
		Deps:      c.DependsOn.SortedIDs(),
		Sensitive: c.Sensitive,
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
	unlock, err := c.acquireCrontabLock()
	if err != nil {
		return err
	}
	defer func() { _ = unlock() }()

	return c.reconcile(id)
}

func (c *Cron) reconcile(id string) error {
	current, err := c.readCrontab()
	if err != nil {
		return err
	}

	desired := ""
	if !c.Absent {
		desired = c.block()
	}

	adopted, adoptedChanged := adoptUnmanaged(current, c.adoption())
	newTab, changed := mergeCrontab(adopted, c.name, desired)
	changed = changed || adoptedChanged
	if !changed {
		resource.Note(id, resource.StatusOK)
		return nil
	}

	desc := fmt.Sprintf("update crontab for %s (job %s)", c.user, c.name)
	return resource.Mutate(id, desc, func() error {
		if err := c.writeCrontab(newTab); err != nil {
			return err
		}
		logger.Info("updated crontab for %s (job %s)", c.user, c.name)
		return nil
	})
}

// adoption returns the unmanaged entries c takes over (see adoption): its
// WithLegacyCommand, plus, for a present job without WithCronEnv, any
// entry identical to its own schedule and command. A job with WithCronEnv
// runs with extra variables, so an unmanaged line without them is not the
// same job and is left alone (pass WithLegacyCommand to adopt it anyway).
// An absent job adopts nothing: NoCron removes only its own block.
func (c *Cron) adoption() adoption {
	a := adoption{legacy: c.legacy}
	if !c.Absent && len(c.env) == 0 {
		a.identical = &cronEntry{
			fields:  [5]string{c.minute, c.hour, c.monthday, c.month, c.weekday},
			command: c.command,
		}
	}
	return a
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

// acquireCrontabLock takes the write lock for c's crontab: the
// cross-process lock (lock.go), or an in-process one (lockCrontabInProcess)
// while c's crontab is faked and the injected runners did not ask for the
// real lock (see runners.CronRunners).
func (c *Cron) acquireCrontabLock() (func() error, error) {
	if c.inProcessLock {
		return lockCrontabInProcess(c.user)
	}
	return lockCrontab(c.user)
}
