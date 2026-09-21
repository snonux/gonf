package user

import (
	"errors"
	"strings"
	"testing"

	"github.com/snonux/gonf/resource"
)

// fixedRunner answers every command with the same result.
func fixedRunner(stdout, stderr string, code int, err error) Runner {
	return func(string, ...string) (string, string, int, error) { return stdout, stderr, code, err }
}

// TestCommandErrorTextIsByteIdentical pins the exact error text of the shared
// command plumbing, which every backend now routes through (task 572). The
// strings are the ones each backend produced with its own copy before.
func TestCommandErrorTextIsByteIdentical(t *testing.T) {
	startFails := fixedRunner("", "", 0, errors.New("boom"))
	exits3 := fixedRunner("out", "err", 3, nil)
	tests := []struct {
		name   string
		runner Runner
		call   func(commands) error
		want   string
	}{
		{"probe start failure", startFails, func(c commands) error { _, err := c.probe("id", "-Gn", "svc"); return err }, "id -Gn svc: boom"},
		{"probe exit keeps stdout and stderr", exits3, func(c commands) error { _, err := c.probe("pw", "groupshow", "-a"); return err }, "pw groupshow -a failed (exit 3): outerr"},
		{"getent start failure", startFails, func(c commands) error { _, _, err := c.getent("passwd", "svc"); return err }, "getent passwd svc: boom"},
		// getent exit 1 (bad arguments or an unsupported database) is an error,
		// never "key not found": only exit 2 means a missing entry.
		{"getent exit 1 is an error", fixedRunner("", "err", 1, nil), func(c commands) error { _, _, err := c.getent("passwd", "svc"); return err }, "getent passwd svc failed (exit 1): err"},
		{"getent exit omits stdout", exits3, func(c commands) error { _, _, err := c.getent("group", "svc"); return err }, "getent group svc failed (exit 3): err"},
		{"idGroups exit", exits3, func(c commands) error { _, err := c.idGroups("--groups", "--name", "svc"); return err }, "id --groups --name svc failed (exit 3): outerr"},
		{"mutate exit", exits3, func(c commands) error { return c.mutate("User[svc]", "useradd", "svc") }, "useradd svc failed (exit 3): outerr"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.call(commands{run: tt.runner})
			if err == nil || err.Error() != tt.want {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestFreeBSDPwShowErrorTextIsByteIdentical(t *testing.T) {
	b := NewFreeBSD(fixedRunner("out", "err", 3, nil))
	if _, _, err := b.pwShow("usershow", "svc"); err == nil || err.Error() != "pw usershow -n svc failed (exit 3): outerr" {
		t.Fatalf("usershow error = %v", err)
	}
	b = NewFreeBSD(fixedRunner("", "", 0, errors.New("boom")))
	if _, err := b.groupExists("svc"); err == nil || err.Error() != "pw groupshow -n svc: boom" {
		t.Fatalf("groupshow error = %v", err)
	}
	b = NewFreeBSD(fixedRunner("", "", freeBSDNoUserExit, nil))
	if _, found, err := b.pwShow("usershow", "svc"); err != nil || found {
		t.Fatalf("EX_NOUSER = found %v, err %v; want missing, nil", found, err)
	}
}

// TestMutateRunsOnlyOutsideDryRun pins the shared mutation path: it records
// the ID as changed, runs the command outside dry-run, and never runs it in
// dry-run.
func TestMutateRunsOnlyOutsideDryRun(t *testing.T) {
	original := resource.DryRun()
	t.Cleanup(func() {
		resource.SetDryRun(original)
		resource.ResetReport()
	})
	for _, dryRun := range []bool{false, true} {
		resource.ResetReport()
		resource.SetDryRun(dryRun)
		ran := false
		c := commands{run: func(string, ...string) (string, string, int, error) { ran = true; return "", "", 0, nil }}
		if err := c.mutate("User[svc]", "useradd", "svc"); err != nil {
			t.Fatalf("dryRun=%v: mutate() = %v", dryRun, err)
		}
		if ran == dryRun {
			t.Fatalf("dryRun=%v: command ran = %v", dryRun, ran)
		}
		if !resource.AnyChanged("User[svc]") {
			t.Fatalf("dryRun=%v: mutation not reported", dryRun)
		}
	}
}

func TestParseFreeBSDGroupRejectsMalformedEntries(t *testing.T) {
	for line, want := range map[string]string{
		"wheel:*:0":        `pw groupshow -a returned malformed group entry "wheel:*:0"`,
		"bad group:*:0:":   "contains whitespace",
		"wheel:*:notanum:": `pw groupshow -a returned invalid gid "notanum"`,
	} {
		_, _, _, err := parseFreeBSDGroup(line)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("parseFreeBSDGroup(%q) = %v, want %q", line, err, want)
		}
	}
}
