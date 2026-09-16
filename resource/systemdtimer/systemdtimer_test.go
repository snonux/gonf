package systemdtimer

import (
	"strings"
	"testing"

	opt "github.com/snonux/gonf/api/options"
)

func TestUnitContent(t *testing.T) {
	t.Parallel()
	tm := newTimer("unattended-upgrade-rocky",
		opt.WithCommand("/usr/local/sbin/unattended-upgrade-rocky daily"),
		opt.WithOnCalendar("*-*-* *:05:00"),
		opt.WithOnBootSec("10min"),
		opt.WithPersistent,
		opt.WithDescription("Hourly unattended-upgrade check (updates once per day)"),
		opt.WithServiceDescription("Unattended upgrade (Rocky daily mode)"),
		opt.WithAfter("network-online.target"),
		opt.WithWants("network-online.target"),
	)

	svc := tm.serviceUnit()
	for _, want := range []string{
		"[Unit]\n",
		"Description=Unattended upgrade (Rocky daily mode)\n",
		"Wants=network-online.target\n",
		"After=network-online.target\n",
		"[Service]\n",
		"Type=oneshot\n",
		"ExecStart=/usr/local/sbin/unattended-upgrade-rocky daily\n",
	} {
		if !strings.Contains(svc, want) {
			t.Errorf("service unit missing %q\n%s", want, svc)
		}
	}

	timer := tm.timerUnit()
	for _, want := range []string{
		"Description=Hourly unattended-upgrade check (updates once per day)\n",
		"OnBootSec=10min\n",
		"OnCalendar=*-*-* *:05:00\n",
		"Persistent=true\n",
		"WantedBy=timers.target\n",
	} {
		if !strings.Contains(timer, want) {
			t.Errorf("timer unit missing %q\n%s", want, timer)
		}
	}
}

func TestValidateRequiresCommandAndCalendar(t *testing.T) {
	t.Parallel()
	if err := newTimer("x").validate(); err == nil || !strings.Contains(err.Error(), "WithCommand") {
		t.Fatalf("want WithCommand required, got %v", err)
	}
	if err := newTimer("x", opt.WithCommand("true")).validate(); err == nil || !strings.Contains(err.Error(), "WithOnCalendar") {
		t.Fatalf("want WithOnCalendar required, got %v", err)
	}
	if err := newTimer("x",
		opt.WithCommand("true"),
		opt.WithOnCalendar("daily"),
	).validate(); err != nil {
		t.Fatalf("valid present: %v", err)
	}
	if err := newTimer("x", opt.IsAbsent).validate(); err != nil {
		t.Fatalf("absent needs no command: %v", err)
	}
}

func TestNormalizeName(t *testing.T) {
	t.Parallel()
	base, unit := normalizeName("job.timer")
	if base != "job" || unit != "job.timer" {
		t.Fatalf("got %q %q", base, unit)
	}
	base, unit = normalizeName("job.service")
	if base != "job" || unit != "job.timer" {
		t.Fatalf("got %q %q", base, unit)
	}
}
