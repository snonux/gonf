package cron

import (
	"os"
	"strings"
	"testing"

	opt "github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/resource"
)

func TestMergeCrontabAddReplaceRemove(t *testing.T) {
	c := &Cron{
		name: "job", user: "root", command: "/bin/true",
		minute: "0", hour: "1", monthday: "*", month: "*", weekday: "*",
	}
	desired := c.block()

	out, changed := mergeCrontab("", "job", desired)
	if !changed || !strings.Contains(out, "/bin/true") {
		t.Fatalf("add: changed=%v out=%q", changed, out)
	}

	out2, changed2 := mergeCrontab(out, "job", desired)
	if changed2 {
		t.Fatal("idempotent replace should not change")
	}

	c.minute = "5"
	desired2 := c.block()
	out3, changed3 := mergeCrontab(out2, "job", desired2)
	if !changed3 || !strings.Contains(out3, "5 1 * * * /bin/true") {
		t.Fatalf("replace: %q", out3)
	}

	out4, changed4 := mergeCrontab(out3, "job", "")
	if !changed4 || strings.Contains(out4, "GONF Cron[job]") {
		t.Fatalf("remove: %q", out4)
	}
}

func TestMergePreservesOtherLines(t *testing.T) {
	existing := "MAILTO=root\n0 * * * * /bin/echo keep\n"
	c := &Cron{
		name: "gonfjob", user: "root", command: "/bin/true",
		minute: "*", hour: "*", monthday: "*", month: "*", weekday: "*",
	}
	out, _ := mergeCrontab(existing, "gonfjob", c.block())
	if !strings.Contains(out, "/bin/echo keep") || !strings.Contains(out, "MAILTO=root") {
		t.Fatalf("lost existing lines: %q", out)
	}
}

func TestPresentRequiresCommand(t *testing.T) {
	resource.ResetRepository()
	Present("x")
	if err := resource.Apply(); err == nil {
		t.Fatal("expected error without WithCommand")
	}
}

func TestPresentRejectsWhitespaceName(t *testing.T) {
	resource.ResetRepository()
	Present("bad name", opt.WithCommand("/bin/true"))
	if err := resource.Apply(); err == nil {
		t.Fatal("expected error for whitespace in name")
	}
}

func TestLiveCronRoundTrip(t *testing.T) {
	if os.Getenv("GONF_RUN_CRON_TESTS") != "1" {
		t.Skip("set GONF_RUN_CRON_TESTS=1 for live crontab tests")
	}
	resource.ResetRepository()
	name := "gonf-live-test"
	Present(name,
		opt.WithCronUser("root"),
		opt.WithCommand("/bin/true"),
		opt.WithMinute("7"),
		opt.WithHour("3"),
	)
	if err := resource.Apply(); err != nil {
		t.Fatalf("present: %v", err)
	}

	resource.ResetRepository()
	Present(name,
		opt.WithCronUser("root"),
		opt.WithCommand("/bin/true"),
		opt.WithMinute("7"),
		opt.WithHour("3"),
	)
	if err := resource.Apply(); err != nil {
		t.Fatalf("idempotent: %v", err)
	}

	resource.ResetRepository()
	Absent(name, opt.WithCronUser("root"))
	if err := resource.Apply(); err != nil {
		t.Fatalf("absent: %v", err)
	}
}

func TestLiveCronPerUser(t *testing.T) {
	if os.Getenv("GONF_RUN_CRON_TESTS") != "1" {
		t.Skip("set GONF_RUN_CRON_TESTS=1 for live crontab tests")
	}
	user := os.Getenv("USER")
	if user == "" || user == "root" {
		t.Skip("need non-root USER for per-user crontab test")
	}
	resource.ResetRepository()
	name := "gonf-live-user"
	Present(name,
		opt.WithCronUser(user),
		opt.WithCommand("/bin/true"),
		opt.WithMinute("11"),
		opt.WithHour("4"),
		opt.WithCronEnv("GONF_CRON_TEST=1"),
	)
	if err := resource.Apply(); err != nil {
		t.Fatalf("present: %v", err)
	}
	resource.ResetRepository()
	Absent(name, opt.WithCronUser(user))
	if err := resource.Apply(); err != nil {
		t.Fatalf("absent: %v", err)
	}
}
