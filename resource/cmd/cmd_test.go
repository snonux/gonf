package cmd

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	opt "github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/internal/exec"
	"github.com/snonux/gonf/internal/runners"
	"github.com/snonux/gonf/internal/testapply"
	"github.com/snonux/gonf/resource"
)

func TestCreatesSkipsWhenPathExists(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	marker := filepath.Join(dir, "done")
	if err := os.WriteFile(marker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	out := filepath.Join(dir, "should-not-exist")
	Present("touch", []string{out}, opt.Creates(marker), opt.WithName("creates-skip"))
	if err := testapply.Apply(); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatalf("command should have been skipped, but %s exists", out)
	}
}

func TestCreatesRunsWhenPathMissing(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	marker := filepath.Join(dir, "missing")
	out := filepath.Join(dir, "created")

	Present("touch", []string{out}, opt.Creates(marker), opt.WithName("creates-run"))
	if err := testapply.Apply(); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if _, err := os.Stat(out); err != nil {
		t.Fatalf("expected %s to exist: %v", out, err)
	}
}

func TestOnChangeRunsCommandOnlyForChangedDependency(t *testing.T) {
	tests := []struct {
		name    string
		status  resource.Status
		wantRun bool
	}{
		{name: "unchanged dependency skips", status: resource.StatusOK},
		{name: "changed dependency runs", status: resource.StatusChanged, wantRun: true},
		{name: "dry run change runs", status: resource.StatusWouldChange, wantRun: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resource.ResetRepository()
			out := filepath.Join(t.TempDir(), "ran")
			watched := testapply.Register("File", "unit", testapply.Noting(tt.status, "File[unit]"))
			Present("touch", []string{out}, opt.OnChange(watched))

			if err := testapply.Apply(); err != nil {
				t.Fatalf("Apply: %v", err)
			}
			_, err := os.Stat(out)
			if tt.wantRun && err != nil {
				t.Fatalf("gated command did not run: %v", err)
			}
			if !tt.wantRun && !os.IsNotExist(err) {
				t.Fatalf("gated command ran without a changed dependency: %v", err)
			}
		})
	}
}

func TestUnlessSkipsOnSuccess(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	out := filepath.Join(dir, "should-not-exist")

	Present("touch", []string{out},
		opt.Unless("true", nil),
		opt.WithName("unless-skip"),
	)
	if err := testapply.Apply(); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatalf("command should have been skipped")
	}
}

func TestUnlessRunsOnFailure(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	out := filepath.Join(dir, "created")

	Present("touch", []string{out},
		opt.Unless("false", nil),
		opt.WithName("unless-run"),
	)
	if err := testapply.Apply(); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if _, err := os.Stat(out); err != nil {
		t.Fatalf("expected %s: %v", out, err)
	}
}

func TestOnlyIfSkipsOnFailure(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	out := filepath.Join(dir, "should-not-exist")

	Present("touch", []string{out},
		opt.OnlyIf("false", nil),
		opt.WithName("onlyif-skip"),
	)
	if err := testapply.Apply(); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatalf("command should have been skipped")
	}
}

func TestOnlyIfRunsOnSuccess(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	out := filepath.Join(dir, "created")

	Present("touch", []string{out},
		opt.OnlyIf("true", nil),
		opt.WithName("onlyif-run"),
	)
	if err := testapply.Apply(); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if _, err := os.Stat(out); err != nil {
		t.Fatalf("expected %s: %v", out, err)
	}
}

func TestUnlessExpectStdout(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	out := filepath.Join(dir, "should-not-exist")

	Present("touch", []string{out},
		opt.Unless("echo", []string{"hello"}, opt.ExpectStdout("hello")),
		opt.WithName("unless-stdout"),
	)
	if err := testapply.Apply(); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatalf("command should have been skipped")
	}
}

func TestCommandFailure(t *testing.T) {
	resource.ResetRepository()
	Present("false", nil, opt.WithName("fail"))
	if err := testapply.Apply(); err == nil {
		t.Fatal("expected Apply to fail")
	}
}

func TestWithDir(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	Present("touch", []string{"in-dir"},
		opt.WithDir(dir),
		opt.WithName("with-dir"),
	)
	if err := testapply.Apply(); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "in-dir")); err != nil {
		t.Fatalf("expected file in dir: %v", err)
	}
}

func TestWithEnv(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	out := filepath.Join(dir, "env.txt")
	// sh -c writes $GONF_TEST_ENV into out
	Present("sh", []string{"-c", "printf '%s' \"$GONF_TEST_ENV\" > " + out},
		opt.WithEnv(map[string]string{"GONF_TEST_ENV": "from-gonf"}),
		opt.WithName("with-env"),
	)
	if err := testapply.Apply(); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "from-gonf" {
		t.Fatalf("env = %q, want from-gonf", got)
	}
}

func TestDefaultName(t *testing.T) {
	if got := defaultName("echo", []string{"a", "b"}); got != "echo a b" {
		t.Fatalf("defaultName = %q", got)
	}
	if got := defaultName("true", nil); got != "true" {
		t.Fatalf("defaultName = %q", got)
	}
}

func TestGuardPassesSemantics(t *testing.T) {
	tests := []struct {
		name        string
		guard       *opt.Guard
		probeStdout string
		probeExit   int
		wantPass    bool
	}{
		{
			name:      "default-expect-zero-matches",
			guard:     &opt.Guard{Name: "probe"},
			probeExit: 0,
			wantPass:  true,
		},
		{
			name:      "default-expect-zero-mismatch",
			guard:     &opt.Guard{Name: "probe"},
			probeExit: 1,
			wantPass:  false,
		},
		{
			name:      "expect-exit-matches",
			guard:     &opt.Guard{Name: "probe", ExpectExit: 3},
			probeExit: 3,
			wantPass:  true,
		},
		{
			name:      "expect-exit-mismatch",
			guard:     &opt.Guard{Name: "probe", ExpectExit: 2},
			probeExit: 3,
			wantPass:  false,
		},
		{
			name:        "expect-stdout-trimmed-match",
			guard:       &opt.Guard{Name: "probe", ExpectStdout: "ready"},
			probeStdout: "  ready \n",
			probeExit:   0,
			wantPass:    true,
		},
		{
			name:        "expect-stdout-mismatch",
			guard:       &opt.Guard{Name: "probe", ExpectStdout: "ready"},
			probeStdout: "other",
			probeExit:   0,
			wantPass:    false,
		},
		{
			name:        "expect-stdout-and-exit",
			guard:       &opt.Guard{Name: "probe", ExpectExit: 3, ExpectStdout: "ready"},
			probeStdout: "ready",
			probeExit:   3,
			wantPass:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &Cmd{probeFn: func(name string, args ...string) (string, string, int, error) {
				if name != "probe" {
					t.Errorf("probe name = %q, want probe", name)
				}
				return tt.probeStdout, "", tt.probeExit, nil
			}}

			got, err := c.guardPasses(tt.guard)
			if err != nil {
				t.Fatalf("guardPasses: %v", err)
			}
			if got != tt.wantPass {
				t.Fatalf("guardPasses = %v, want %v", got, tt.wantPass)
			}
		})
	}
}

// guardPasses must run the probe with the exact argv from the Guard.
func TestGuardPassesProbeArgv(t *testing.T) {
	c := &Cmd{probeFn: func(name string, args ...string) (string, string, int, error) {
		if name != "check" || !slices.Equal(args, []string{"-x", "y"}) {
			t.Errorf("probe argv = %q %v, want check [-x y]", name, args)
		}
		return "", "", 0, nil
	}}

	ok, err := c.guardPasses(&opt.Guard{Name: "check", Args: []string{"-x", "y"}})
	if err != nil {
		t.Fatalf("guardPasses: %v", err)
	}
	if !ok {
		t.Fatal("guardPasses = false, want true")
	}
}

// A failing probe must surface its error; the main command must not run.
func TestGuardPassesProbeError(t *testing.T) {
	c := &Cmd{
		runFn: func(opts exec.Opts, name string, args ...string) (string, string, int, error) {
			t.Error("main command must not run when the probe errors")
			return "", "", 0, nil
		},
		probeFn: func(name string, args ...string) (string, string, int, error) {
			return "", "", -1, errors.New("probe exploded")
		},
	}

	_, err := c.guardPasses(&opt.Guard{Name: "probe"})
	if err == nil || !strings.Contains(err.Error(), "probe exploded") {
		t.Fatalf("err = %v, want probe exploded", err)
	}
}

// Unless passing → skip the main command (probe runs, main never does).
func TestUnlessPassingSkipsMainCommand(t *testing.T) {
	resource.ResetRepository()
	probeArgv := ""
	rs := &runners.Set{Command: &runners.CommandRunners{
		Run: func(opts exec.Opts, name string, args ...string) (string, string, int, error) {
			t.Errorf("main command %q must not run when the unless guard passes", name)
			return "", "", 0, nil
		},
		Probe: func(name string, args ...string) (string, string, int, error) {
			probeArgv = name + " " + strings.Join(args, " ")
			return "", "", 0, nil
		},
	}}

	out := filepath.Join(t.TempDir(), "should-not-exist")
	Present("touch", []string{out},
		opt.Unless("always-true", []string{"flag"}),
		opt.WithName("unless-skip-fake"),
	)
	if err := testapply.ApplyWithRunners(rs); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if probeArgv != "always-true flag" {
		t.Fatalf("probe argv = %q, want %q", probeArgv, "always-true flag")
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatalf("command should have been skipped, but %s exists", out)
	}
}

// OnlyIf failing → skip the main command.
func TestOnlyIfFailingSkipsMainCommand(t *testing.T) {
	resource.ResetRepository()
	rs := &runners.Set{Command: &runners.CommandRunners{
		Run: func(opts exec.Opts, name string, args ...string) (string, string, int, error) {
			t.Errorf("main command %q must not run when the onlyIf guard fails", name)
			return "", "", 0, nil
		},
		Probe: func(name string, args ...string) (string, string, int, error) {
			return "", "", 1, nil
		},
	}}

	out := filepath.Join(t.TempDir(), "should-not-exist")
	Present("touch", []string{out},
		opt.OnlyIf("always-false", nil),
		opt.WithName("onlyif-skip-fake"),
	)
	if err := testapply.ApplyWithRunners(rs); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatalf("command should have been skipped, but %s exists", out)
	}
}

// Guard probe errors are wrapped with the guard kind and resource id.
func TestGuardErrorWrapped(t *testing.T) {
	resource.ResetRepository()
	rs := &runners.Set{Command: &runners.CommandRunners{
		Run: func(opts exec.Opts, name string, args ...string) (string, string, int, error) {
			t.Error("main command must not run when the guard errors")
			return "", "", 0, nil
		},
		Probe: func(name string, args ...string) (string, string, int, error) {
			return "", "", -1, errors.New("probe exploded")
		},
	}}

	Present("true", nil, opt.Unless("probe", nil), opt.WithName("unless-err"))
	err := testapply.ApplyWithRunners(rs)
	if err == nil || !strings.Contains(err.Error(),
		"unless guard for Command[unless-err]: probe exploded") {
		t.Fatalf("err = %v, want wrapped unless guard error", err)
	}
}

// An existing creates path short-circuits apply before the runner is reached.
func TestCreatesExistingSkipsRunner(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	marker := filepath.Join(dir, "done")
	if err := os.WriteFile(marker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	runs := 0
	rs := &runners.Set{Command: &runners.CommandRunners{Run: func(opts exec.Opts, name string, args ...string) (string, string, int, error) {
		runs++
		return "", "", 0, nil
	}}}

	out := filepath.Join(dir, "should-not-exist")
	Present("touch", []string{out}, opt.Creates(marker), opt.WithName("creates-skip-fake"))
	if err := testapply.ApplyWithRunners(rs); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if runs != 0 {
		t.Fatalf("runner invoked %d times, want 0", runs)
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatalf("command should have been skipped, but %s exists", out)
	}
}

// A missing creates path falls through to exactly one runner invocation.
func TestCreatesMissingInvokesRunnerOnce(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	marker := filepath.Join(dir, "missing")

	runs := 0
	rs := &runners.Set{Command: &runners.CommandRunners{Run: func(opts exec.Opts, name string, args ...string) (string, string, int, error) {
		runs++
		return "", "", 0, nil
	}}}

	out := filepath.Join(dir, "fake-created")
	Present("touch", []string{out}, opt.Creates(marker), opt.WithName("creates-run-fake"))
	if err := testapply.ApplyWithRunners(rs); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if runs != 1 {
		t.Fatalf("runner invoked %d times, want 1", runs)
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatalf("fake runner must not create %s", out)
	}
}

// A relative Creates path is resolved against WithDir, where the command
// runs and so creates it, not against Gonf's own working directory: an
// existing <dir>/<creates> skips the command, a missing one runs it.
func TestCreatesRelativeResolvesAgainstDir(t *testing.T) {
	for _, tt := range []struct {
		name     string
		existing bool
		wantRuns int
	}{
		{name: "existing skips", existing: true, wantRuns: 0},
		{name: "missing runs", existing: false, wantRuns: 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			resource.ResetRepository()
			dir := t.TempDir()
			if tt.existing {
				if err := os.WriteFile(filepath.Join(dir, "build.done"), nil, 0o644); err != nil {
					t.Fatal(err)
				}
			}

			runs := 0
			rs := &runners.Set{Command: &runners.CommandRunners{Run: func(opts exec.Opts, name string, args ...string) (string, string, int, error) {
				runs++
				return "", "", 0, nil
			}}}

			Present("true", nil, opt.WithDir(dir), opt.Creates("build.done"), opt.WithName("creates-relative"))
			if err := testapply.ApplyWithRunners(rs); err != nil {
				t.Fatalf("Apply: %v", err)
			}
			if runs != tt.wantRuns {
				t.Fatalf("runner invoked %d times, want %d", runs, tt.wantRuns)
			}
		})
	}
}

// The happy path must plumb argv, Dir, and the merged Env into the runner.
func TestRunPlumbsArgsEnvDirToRunner(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()

	type runCall struct {
		opts exec.Opts
		name string
		args []string
	}
	var got runCall
	probes := 0
	rs := &runners.Set{Command: &runners.CommandRunners{
		Run: func(opts exec.Opts, name string, args ...string) (string, string, int, error) {
			got = runCall{opts: opts, name: name, args: args}
			return "main stdout\n", "", 0, nil
		},
		Probe: func(name string, args ...string) (string, string, int, error) {
			probes++
			return "", "", 0, nil
		},
	}}

	Present("mybin", []string{"a1", "a2"},
		opt.WithDir(dir),
		opt.WithEnv(map[string]string{"GONF_FAKE_ENV": "plumbed"}),
		opt.WithName("plumbed"),
	)
	if err := testapply.ApplyWithRunners(rs); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if got.name != "mybin" || !slices.Equal(got.args, []string{"a1", "a2"}) {
		t.Fatalf("argv = %q %v, want mybin [a1 a2]", got.name, got.args)
	}
	if got.opts.Dir != dir {
		t.Fatalf("opts.Dir = %q, want %q", got.opts.Dir, dir)
	}
	env := map[string]string{}
	for _, kv := range got.opts.Env {
		if k, v, ok := strings.Cut(kv, "="); ok {
			env[k] = v
		}
	}
	if env["GONF_FAKE_ENV"] != "plumbed" {
		t.Fatalf("opts.Env = %v, want GONF_FAKE_ENV=plumbed", got.opts.Env)
	}
	if _, ok := env["PATH"]; !ok {
		t.Fatal("expected the inherited environment (PATH) in opts.Env")
	}
	if probes != 0 {
		t.Fatalf("probe invoked %d times without guards", probes)
	}
}

// A non-zero exit from the runner produces the historical error message.
func TestRunNonZeroExitErrorMessage(t *testing.T) {
	resource.ResetRepository()
	rs := &runners.Set{Command: &runners.CommandRunners{Run: func(opts exec.Opts, name string, args ...string) (string, string, int, error) {
		return "some stdout\n", "some stderr\n", 7, nil
	}}}

	Present("failing-bin", nil, opt.WithName("exit7"))
	err := testapply.ApplyWithRunners(rs)
	// run() embeds the raw captured streams, so the fake's trailing newline
	// shows up as a blank line before "stderr:".
	want := "failing-bin exited 7\nstdout: some stdout\n\nstderr: some stderr\n"
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("err = %v, want substring %q", err, want)
	}
}

// A start failure from the runner is wrapped with "failed to execute".
func TestRunStartFailureWrapped(t *testing.T) {
	resource.ResetRepository()
	rs := &runners.Set{Command: &runners.CommandRunners{Run: func(opts exec.Opts, name string, args ...string) (string, string, int, error) {
		return "", "", -1, errors.New("fork/exec: no such file or directory")
	}}}

	Present("gone-bin", nil, opt.WithName("startfail"))
	err := testapply.ApplyWithRunners(rs)
	if err == nil || !strings.Contains(err.Error(),
		"failed to execute gone-bin: fork/exec: no such file or directory") {
		t.Fatalf("err = %v, want wrapped start failure", err)
	}
}

// Dry-run must short-circuit run() before the runner is invoked.
func TestRunDryRunDoesNotInvokeRunner(t *testing.T) {
	resource.ResetRepository()
	resource.SetDryRun(true)
	defer resource.SetDryRun(false)

	runs := 0
	rs := &runners.Set{Command: &runners.CommandRunners{Run: func(opts exec.Opts, name string, args ...string) (string, string, int, error) {
		runs++
		return "", "", 0, nil
	}}}

	Present("mybin", []string{"x"}, opt.WithName("dry"))
	if err := testapply.ApplyWithRunners(rs); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if runs != 0 {
		t.Fatalf("runner invoked %d times in dry-run, want 0", runs)
	}
}
