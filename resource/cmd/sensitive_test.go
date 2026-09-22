package cmd

import (
	"strings"
	"testing"

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
	SetRunnersForTest(func(exec.Opts, string, ...string) (string, string, int, error) {
		return "echo " + fakeCmdSecret, "denied " + fakeCmdSecret, 7, nil
	}, nil)
	t.Cleanup(ResetRunnersForTest)
	output, restore := logger.CaptureForTest(logger.LevelDebug)
	t.Cleanup(restore)

	op := plan.Op{Op: plan.KindCommand, ID: "Command[upload]", Name: "upload", Bin: "/usr/bin/curl",
		Args: []string{"-H", "Authorization: Bearer " + fakeCmdSecret}, Sensitive: true}
	err := planHandler{}.Apply(op, plan.ApplyContext{})
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
	err = planHandler{}.Apply(op, plan.ApplyContext{})
	if err == nil || !strings.Contains(err.Error(), "denied "+fakeCmdSecret) {
		t.Fatalf("plain command: err = %v, want its output", err)
	}
}

// WithSensitive gives a direct command (no plan involved) the same
// withholding as a sensitive op, and records a sensitive draft.
func TestWithSensitiveCommandWithholdsArgvDirectly(t *testing.T) {
	resource.ResetForTest()
	t.Cleanup(resource.ResetForTest)
	SetRunnersForTest(func(exec.Opts, string, ...string) (string, string, int, error) {
		return "", "denied " + fakeCmdSecret, 7, nil
	}, nil)
	t.Cleanup(ResetRunnersForTest)
	output, restore := logger.CaptureForTest(logger.LevelDebug)
	t.Cleanup(restore)

	args := []string{"-H", "Authorization: Bearer " + fakeCmdSecret}
	err := Ensure("/usr/bin/curl", args, opt.WithName("upload"), opt.WithSensitive)
	if err == nil || strings.Contains(err.Error(), fakeCmdSecret) || strings.Contains(output(), fakeCmdSecret) {
		t.Fatalf("direct sensitive command leaks: err = %v, log:\n%s", err, output())
	}
	c := &Cmd{bin: "/usr/bin/curl", args: args}
	opt.WithSensitive.Apply(c)
	if !c.planDraft("Command[upload]").Sensitive {
		t.Fatal("WithSensitive did not reach the command's plan draft")
	}
}
