package api

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gexec "github.com/snonux/gonf/internal/exec"
	"github.com/snonux/gonf/internal/privilege"
	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
)

// TestElevatedApplyArgvDryRun pins the argv-level fix for the bug where a
// local "gonf -n"/"-dry-run" run re-exec'd a Privileged() chunk via
// sudo/doas WITHOUT "-n": the elevated child then applied for real. dry-run
// must add "-n" right after "apply"; non-dry-run must not add it at all.
func TestElevatedApplyArgvDryRun(t *testing.T) {
	got := elevatedApplyArgv("/usr/local/bin/gonf", "/tmp/plan/chunk-elevated.jsonl", true, "", gexec.BuiltinDefaultTimeout)
	want := []string{"/usr/local/bin/gonf", "apply", "-n", "/tmp/plan/chunk-elevated.jsonl"}
	if len(got) != len(want) {
		t.Fatalf("dry-run argv = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("dry-run argv = %v, want %v", got, want)
		}
	}

	got = elevatedApplyArgv("/usr/local/bin/gonf", "/tmp/plan/chunk-elevated.jsonl", false, "", gexec.BuiltinDefaultTimeout)
	want = []string{"/usr/local/bin/gonf", "apply", "/tmp/plan/chunk-elevated.jsonl"}
	if len(got) != len(want) {
		t.Fatalf("non-dry-run argv = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("non-dry-run argv = %v, want %v", got, want)
		}
	}
	for _, arg := range got {
		if arg == "-n" {
			t.Fatalf("non-dry-run argv must not contain -n: %v", got)
		}
	}
}

// TestElevatedApplyArgvProfileOverride pins the argv-level fix for the bug
// where a local "gonf -profile=<override> -privilege=sudo/doas" run
// re-exec'd a Privileged() chunk WITHOUT the override: the elevated child
// then called DetectFacts() fresh and re-derived the profile from the actual
// host, so when_begin{fact:profile,...} guards evaluated inconsistently
// between the unprivileged (in-process, with override) and privileged
// (re-exec'd, without override) chunks of the same plan. An active override
// must add "-profile=<value>" as a GLOBAL flag ahead of "apply" (per
// internal/cli/cli.go: "-profile" is parsed by the top-level flag set, not
// by cliApply's "apply" subcommand flag set); no override must add nothing,
// preserving auto-detect. Both dry-run and profile-override must be able to
// combine, with "-profile" before "apply" and "-n" right after it.
func TestElevatedApplyArgvProfileOverride(t *testing.T) {
	got := elevatedApplyArgv("/usr/local/bin/gonf", "/tmp/plan/chunk-elevated.jsonl", false, "rocky", gexec.BuiltinDefaultTimeout)
	want := []string{"/usr/local/bin/gonf", "-profile=rocky", "apply", "/tmp/plan/chunk-elevated.jsonl"}
	if len(got) != len(want) {
		t.Fatalf("profile-override argv = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("profile-override argv = %v, want %v", got, want)
		}
	}

	got = elevatedApplyArgv("/usr/local/bin/gonf", "/tmp/plan/chunk-elevated.jsonl", false, "", gexec.BuiltinDefaultTimeout)
	for _, arg := range got {
		if strings.HasPrefix(arg, "-profile=") {
			t.Fatalf("no-override argv must not contain -profile: %v", got)
		}
	}

	got = elevatedApplyArgv("/usr/local/bin/gonf", "/tmp/plan/chunk-elevated.jsonl", true, "rocky", gexec.BuiltinDefaultTimeout)
	want = []string{"/usr/local/bin/gonf", "-profile=rocky", "apply", "-n", "/tmp/plan/chunk-elevated.jsonl"}
	if len(got) != len(want) {
		t.Fatalf("dry-run+profile-override argv = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("dry-run+profile-override argv = %v, want %v", got, want)
		}
	}
}

// TestElevatedApplyArgvCmdTimeout pins task c82: a non-default command
// timeout reaches the elevated child as the GLOBAL flag
// "-cmd-timeout=<d>" ahead of "apply" (with -profile and -n still in their
// places), and the built-in default (or a non-positive value) adds nothing.
func TestElevatedApplyArgvCmdTimeout(t *testing.T) {
	const exe, path = "/usr/local/bin/gonf", "/tmp/plan/chunk-elevated.jsonl"
	tests := []struct {
		name    string
		dryRun  bool
		profile string
		timeout time.Duration
		want    []string
	}{
		{"set", false, "", 30 * time.Second, []string{exe, "-cmd-timeout=30s", "apply", path}},
		{"set with profile and dry-run", true, "rocky", 90 * time.Second,
			[]string{exe, "-profile=rocky", "-cmd-timeout=1m30s", "apply", "-n", path}},
		{"built-in default omitted", false, "", gexec.BuiltinDefaultTimeout, []string{exe, "apply", path}},
		{"zero omitted", false, "", 0, []string{exe, "apply", path}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := elevatedApplyArgv(exe, path, tt.dryRun, tt.profile, tt.timeout)
			if strings.Join(got, " ") != strings.Join(tt.want, " ") {
				t.Fatalf("argv = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestDefaultElevatedApplyForwardsCmdTimeout runs the real
// defaultElevatedApply against a fake sudo that records its argv: an active
// SetCommandTimeout must reach the re-exec'd child as "-cmd-timeout=<d>"
// before "apply", or a validator in the root chunk would run under the
// child's built-in 5m default instead (task c82).
func TestDefaultElevatedApplyForwardsCmdTimeout(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)
	orig := CommandTimeout()
	t.Cleanup(func() { SetCommandTimeout(orig) })
	SetCommandTimeout(42 * time.Second)

	binDir := t.TempDir()
	captured := filepath.Join(binDir, "sudo.args")
	fakeSudo := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + captured + "\nexit 0\n"
	if err := os.WriteFile(filepath.Join(binDir, "sudo"), []byte(fakeSudo), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	ops := []plan.Op{
		{Op: plan.KindPlan, Version: plan.CurrentVersion, ID: "chunks"},
		{Op: plan.KindCommand, Bin: "true", ID: "Command[elevated]", Elevate: true},
	}
	if err := defaultElevatedApply(context.Background(), privilege.Sudo, ops, t.TempDir()); err != nil {
		t.Fatalf("defaultElevatedApply: %v", err)
	}
	raw, err := os.ReadFile(captured)
	if err != nil {
		t.Fatalf("fake sudo was not invoked: %v", err)
	}
	args := strings.Fields(string(raw))
	flagIdx, applyIdx := -1, -1
	for i, a := range args {
		switch a {
		case "-cmd-timeout=42s":
			flagIdx = i
		case "apply":
			applyIdx = i
		}
	}
	if flagIdx == -1 || applyIdx == -1 || flagIdx >= applyIdx {
		t.Fatalf("sudo argv = %v, want \"-cmd-timeout=42s\" before \"apply\"", args)
	}
}

// TestDefaultElevatedApplyDryRunAddsN is an integration-style test of the
// real (unstubbed) defaultElevatedApply: it fakes the "sudo" binary on PATH
// with a script that records its argv instead of ever re-execing gonf, so
// the assertion covers the whole local elevated re-exec path — including
// privilege.WrapArgv's sudo wrapping — without needing real sudo/doas and
// without ever actually mutating anything. Under resource.DryRun(), the
// recorded argv must contain "-n" ahead of the plan path; that is exactly
// what makes the elevated child preview instead of apply for real.
func TestDefaultElevatedApplyDryRunAddsN(t *testing.T) {
	resource.ResetForTest()
	t.Cleanup(resource.ResetForTest)

	binDir := t.TempDir()
	captured := filepath.Join(binDir, "sudo.args")
	fakeSudo := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + captured + "\nexit 0\n"
	if err := os.WriteFile(filepath.Join(binDir, "sudo"), []byte(fakeSudo), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	resource.SetDryRun(true)

	planDir := t.TempDir()
	ops := []plan.Op{
		{Op: plan.KindPlan, Version: plan.CurrentVersion, ID: "chunks"},
		{Op: plan.KindCommand, Bin: "true", ID: "Command[elevated]", Elevate: true},
	}
	if err := defaultElevatedApply(context.Background(), privilege.Sudo, ops, planDir); err != nil {
		t.Fatalf("defaultElevatedApply: %v", err)
	}

	raw, err := os.ReadFile(captured)
	if err != nil {
		t.Fatalf("fake sudo was not invoked: %v", err)
	}
	args := strings.Fields(string(raw))
	if len(args) < 2 || args[0] != "-n" {
		t.Fatalf("sudo argv = %v, want leading \"-n\" (WrapArgv sudo prefix)", args)
	}
	// The two "-n" occurrences: one from WrapArgv's "sudo -n" (no-password
	// sudo, unrelated to dry-run), one from elevatedApplyArgv's dry-run
	// flag threaded to the child's "apply" subcommand.
	count := 0
	for _, a := range args {
		if a == "-n" {
			count++
		}
	}
	if count != 2 {
		t.Fatalf("sudo argv = %v, want exactly two \"-n\" occurrences (sudo -n, apply -n)", args)
	}
}

// TestDefaultElevatedApplyPropagatesProfileOverride is an integration-style
// test of the real (unstubbed) defaultElevatedApply mirroring
// TestDefaultElevatedApplyDryRunAddsN: it fakes "sudo" on PATH to record its
// argv instead of re-execing gonf. With an active SetProfileOverride, the
// recorded argv must carry "-profile=<value>" ahead of "apply" — otherwise
// the elevated child would call DetectFacts() fresh and evaluate
// when_begin{fact:profile,...} guards against the real host instead of the
// override, diverging from the unprivileged chunks applied in-process by the
// same run.
func TestDefaultElevatedApplyPropagatesProfileOverride(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)

	binDir := t.TempDir()
	captured := filepath.Join(binDir, "sudo.args")
	fakeSudo := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + captured + "\nexit 0\n"
	if err := os.WriteFile(filepath.Join(binDir, "sudo"), []byte(fakeSudo), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	SetProfileOverride("rocky")

	planDir := t.TempDir()
	ops := []plan.Op{
		{Op: plan.KindPlan, Version: plan.CurrentVersion, ID: "chunks"},
		{Op: plan.KindCommand, Bin: "true", ID: "Command[elevated]", Elevate: true},
	}
	if err := defaultElevatedApply(context.Background(), privilege.Sudo, ops, planDir); err != nil {
		t.Fatalf("defaultElevatedApply: %v", err)
	}

	raw, err := os.ReadFile(captured)
	if err != nil {
		t.Fatalf("fake sudo was not invoked: %v", err)
	}
	args := strings.Fields(string(raw))

	profileIdx, applyIdx := -1, -1
	for i, a := range args {
		if a == "-profile=rocky" {
			profileIdx = i
		}
		if a == "apply" {
			applyIdx = i
		}
	}
	if profileIdx == -1 {
		t.Fatalf("sudo argv = %v, want \"-profile=rocky\"", args)
	}
	if applyIdx == -1 {
		t.Fatalf("sudo argv = %v, want \"apply\"", args)
	}
	if profileIdx >= applyIdx {
		t.Fatalf("sudo argv = %v, want \"-profile=rocky\" before \"apply\" (global flag must precede subcommand)", args)
	}
}
