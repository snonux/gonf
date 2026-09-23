package cmd

import (
	"strings"
	"testing"

	"github.com/snonux/gonf/internal/declerr"
	"github.com/snonux/gonf/internal/runners"
	"github.com/snonux/gonf/internal/testutil"

	"github.com/snonux/gonf/internal/exec"
	"github.com/snonux/gonf/internal/logger"
	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
	opt "github.com/snonux/gonf/resource/options"
)

// fakeCmdSecret is synthetic secret material passed as an argument.
const fakeCmdSecret = "fake-bearer-token-77aa"

// A sensitive command op (plan apply) withholds its argv from the log and
// its output from a failure, while a plain one keeps both.
func TestSensitiveCommandWithholdsArgvAndOutput(t *testing.T) {
	resource.ResetForTest()
	t.Cleanup(resource.ResetForTest)
	rs := &runners.Set{Command: &runners.CommandRunners{Run: func(exec.Opts, string, ...string) (string, string, int, error) {
		return "echo " + fakeCmdSecret, "denied " + fakeCmdSecret, 7, nil
	}}}
	output := testutil.CaptureLog(t, logger.LevelDebug)

	op := plan.Op{Op: plan.KindCommand, ID: "Command[upload]", Name: "upload", Bin: "/usr/bin/curl",
		Args: []string{"-H", "Authorization: Bearer " + fakeCmdSecret}, Sensitive: true}
	err := planHandler{}.Apply(op, plan.ApplyContext{Runners: rs})
	if err == nil || !strings.Contains(err.Error(), "output withheld") {
		t.Fatalf("err = %v, want the withheld failure", err)
	}
	for what, text := range map[string]string{"error": err.Error(), "log": output()} {
		if strings.Contains(text, fakeCmdSecret) {
			t.Fatalf("%s leaks the secret: %s", what, text)
		}
	}
	if !strings.Contains(output(), "running Command[upload]: /usr/bin/curl [argv withheld: secret material]") {
		t.Fatalf("log lacks the withheld command line:\n%s", output())
	}

	op.Sensitive = false
	resource.ResetForTest()
	err = planHandler{}.Apply(op, plan.ApplyContext{Runners: rs})
	if err == nil || !strings.Contains(err.Error(), "denied "+fakeCmdSecret) {
		t.Fatalf("plain command: err = %v, want its output", err)
	}
}

// WithSensitive gives a direct command (no plan involved) the same
// withholding as a sensitive op, and records a sensitive draft.
func TestWithSensitiveCommandWithholdsArgvDirectly(t *testing.T) {
	resource.ResetForTest()
	t.Cleanup(resource.ResetForTest)
	cr := &runners.CommandRunners{Run: func(exec.Opts, string, ...string) (string, string, int, error) {
		return "", "denied " + fakeCmdSecret, 7, nil
	}}
	output := testutil.CaptureLog(t, logger.LevelDebug)

	args := []string{"-H", "Authorization: Bearer " + fakeCmdSecret}
	err := ensureWith(cr, "/usr/bin/curl", args, []opt.CommandOption{opt.WithName("upload"), opt.WithSensitive})
	if err == nil || strings.Contains(err.Error(), fakeCmdSecret) || strings.Contains(output(), fakeCmdSecret) {
		t.Fatalf("direct sensitive command leaks: err = %v, log:\n%s", err, output())
	}
	c := &Cmd{bin: "/usr/bin/curl", args: args}
	opt.WithSensitive.Apply(c)
	if !c.planDraft("Command[upload]").Sensitive {
		t.Fatal("WithSensitive did not reach the command's plan draft")
	}
}

// WithSensitive needs WithName: an unnamed command's ID is its argv. The
// check refuses exactly the unnamed sensitive command, naming the binary
// but not the argv.
func TestCheckSensitiveNameRefusesUnnamedCommands(t *testing.T) {
	args := []string{"--token", fakeCmdSecret}
	tests := []struct {
		name    string
		opts    []opt.CommandOption
		refused bool
	}{
		{"unnamed sensitive", []opt.CommandOption{opt.WithSensitive}, true},
		{"named sensitive", []opt.CommandOption{opt.WithName("upload"), opt.WithSensitive}, false},
		{"unnamed plain", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &Cmd{bin: "/usr/bin/curl", args: args}
			for _, o := range tt.opts {
				o.Apply(c)
			}
			err := c.checkSensitiveName()
			if (err != nil) != tt.refused {
				t.Fatalf("err = %v, refused want %v", err, tt.refused)
			}
			if err != nil && (strings.Contains(err.Error(), fakeCmdSecret) || !strings.Contains(err.Error(), "WithName")) {
				t.Fatalf("refusal must name WithName and not the argv: %v", err)
			}
		})
	}
}

// Present refuses an unnamed WithSensitive command with a declaration error
// (internal/declerr) that names WithName but not the argv, registers nothing
// for it, and returns a value whose ID carries only the binary; a named one
// registers normally.
func TestPresentRefusesUnnamedSensitiveCommand(t *testing.T) {
	resource.ResetForTest()
	t.Cleanup(resource.ResetForTest)
	if r := Present("/usr/bin/curl", []string{"--token", fakeCmdSecret}, opt.WithName("upload"), opt.WithSensitive); r.ID() != "Command[upload]" {
		t.Fatalf("named sensitive command registered as %s", r.ID())
	}
	if err := declerr.First(); err != nil {
		t.Fatalf("named sensitive command reported %v", err)
	}
	refused := Present("/usr/bin/curl", []string{"--token", fakeCmdSecret}, opt.WithSensitive)
	err := declerr.First()
	if err == nil || !strings.Contains(err.Error(), "WithSensitive requires WithName") {
		t.Fatalf("unnamed sensitive Present reported %v, want the WithName refusal", err)
	}
	if strings.Contains(err.Error(), fakeCmdSecret) || strings.Contains(refused.ID(), fakeCmdSecret) {
		t.Fatalf("refusal leaks the argv: %v / %s", err, refused.ID())
	}
	if ids := resource.RegisteredIDs(); len(ids) != 1 || ids[0] != "Command[upload]" {
		t.Fatalf("registered %v, want only the named command", ids)
	}
}
