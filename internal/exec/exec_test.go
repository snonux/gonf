package exec

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRun(t *testing.T) {
	tests := []struct {
		name         string
		cmd          string
		args         []string
		wantStdout   string
		wantStderr   string
		wantExitCode int
		wantErr      bool
	}{
		{
			name:         "success",
			cmd:          "echo",
			args:         []string{"hello world"},
			wantStdout:   "hello world\n",
			wantStderr:   "",
			wantExitCode: 0,
			wantErr:      false,
		},
		{
			name:         "fail-exit-code",
			cmd:          "ls",
			args:         []string{"/non-existent-directory-12345"},
			wantStdout:   "",
			wantStderr:   "",
			wantExitCode: 2,
			wantErr:      false,
		},
		{
			name:         "fail-binary-not-found",
			cmd:          "non-existent-command-12345",
			args:         []string{},
			wantStdout:   "",
			wantStderr:   "",
			wantExitCode: -1,
			wantErr:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stdout, stderr, exitCode, err := Run(tt.cmd, tt.args...)

			if (err != nil) != tt.wantErr {
				t.Errorf("Run() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if exitCode != tt.wantExitCode {
				t.Errorf("Run() exitCode = %v, want %v", exitCode, tt.wantExitCode)
			}

			if tt.name == "success" && stdout != tt.wantStdout {
				t.Errorf("Run() stdout = %q, want %q", stdout, tt.wantStdout)
			}

			if tt.name == "fail-exit-code" && stderr == "" {
				t.Errorf("Run() stderr = %q, want non-empty", stderr)
			}
		})
	}
}

// A positive Timeout must kill a hung command and surface the deadline as an
// error (not as an exit code, which a killed process would look like).
func TestRunWithTimeout(t *testing.T) {
	start := time.Now()
	stdout, stderr, exitCode, err := RunWith(Opts{Timeout: 50 * time.Millisecond}, "sleep", "5")
	if err == nil {
		t.Fatal("expected a timeout error")
	}
	if !strings.Contains(err.Error(), "context deadline exceeded") {
		t.Fatalf("err = %v, want a context deadline", err)
	}
	if exitCode != -1 {
		t.Fatalf("exitCode = %d, want -1", exitCode)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("RunWith ignored the timeout: took %v", elapsed)
	}
	if stdout != "" || stderr != "" {
		t.Fatalf("stdout=%q stderr=%q, want empty", stdout, stderr)
	}
}

// A command that finishes inside the timeout must behave exactly like the
// no-timeout path (success is not mistaken for a deadline).
func TestRunWithTimeoutSucceeds(t *testing.T) {
	stdout, _, exitCode, err := RunWith(Opts{Timeout: 5 * time.Second}, "echo", "in-time")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if exitCode != 0 || stdout != "in-time\n" {
		t.Fatalf("exit=%d stdout=%q", exitCode, stdout)
	}
}

// withDefaultTimeout temporarily lowers the process-wide default timeout for
// a single test and restores the original value afterwards, so tests can
// prove the default kills a hung command without waiting 5 real minutes and
// without leaking a short default into unrelated tests.
func withDefaultTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	orig := DefaultTimeout()
	SetDefaultTimeout(d)
	t.Cleanup(func() { SetDefaultTimeout(orig) })
}

// Run (the plain, no-Opts entry point every resource backend uses) must kill
// a command that would otherwise hang forever, using the process-wide
// default timeout rather than requiring every caller to opt in per call.
// This is the concrete "a hanging command actually gets killed" proof for
// gonf task x5: without a default, `sleep 5` here would block for 5 seconds
// (or forever for a truly wedged remote command); with the lowered default it
// is killed almost immediately and surfaces a clear timeout error.
func TestRunUsesDefaultTimeout(t *testing.T) {
	withDefaultTimeout(t, 50*time.Millisecond)

	start := time.Now()
	_, _, exitCode, err := Run("sleep", "5")
	if err == nil {
		t.Fatal("expected the default timeout to kill the command")
	}
	if !strings.Contains(err.Error(), "context deadline exceeded") {
		t.Fatalf("err = %v, want a context deadline", err)
	}
	if exitCode != -1 {
		t.Fatalf("exitCode = %d, want -1", exitCode)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("Run ignored the default timeout: took %v", elapsed)
	}
}

// RunWithStdin has no per-call Timeout knob (see its doc comment), so it must
// still be bounded by the process-wide default: a hung command fed from
// stdin (e.g. a wedged `crontab -` write) must not block forever either.
func TestRunWithStdinUsesDefaultTimeout(t *testing.T) {
	withDefaultTimeout(t, 50*time.Millisecond)

	start := time.Now()
	_, _, exitCode, err := RunWithStdin("ignored", "sleep", "5")
	if err == nil {
		t.Fatal("expected the default timeout to kill the command")
	}
	if !strings.Contains(err.Error(), "context deadline exceeded") {
		t.Fatalf("err = %v, want a context deadline", err)
	}
	if exitCode != -1 {
		t.Fatalf("exitCode = %d, want -1", exitCode)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("RunWithStdin ignored the default timeout: took %v", elapsed)
	}
}

// A normal, fast command must be completely unaffected by the default
// timeout being in place (the resilience fix must not regress ordinary
// apply runs).
func TestRunFastCommandUnaffectedByDefaultTimeout(t *testing.T) {
	withDefaultTimeout(t, 50*time.Millisecond)

	stdout, _, exitCode, err := Run("echo", "still fast")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if exitCode != 0 || stdout != "still fast\n" {
		t.Fatalf("exit=%d stdout=%q", exitCode, stdout)
	}
}

// Opts.Timeout < 0 is the explicit, per-call opt-out from any deadline
// (including the process-wide default) for the rare caller that genuinely
// needs it.
func TestRunWithNegativeTimeoutDisablesDeadline(t *testing.T) {
	withDefaultTimeout(t, 50*time.Millisecond)

	stdout, _, exitCode, err := RunWith(Opts{Timeout: -1}, "echo", "unbounded")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if exitCode != 0 || stdout != "unbounded\n" {
		t.Fatalf("exit=%d stdout=%q", exitCode, stdout)
	}
}

// SetDefaultTimeout must reject a non-positive value rather than silently
// leaving Run with an effectively zero (instant-timeout) or unlimited
// default.
func TestSetDefaultTimeoutRejectsNonPositive(t *testing.T) {
	orig := DefaultTimeout()
	t.Cleanup(func() { SetDefaultTimeout(orig) })

	SetDefaultTimeout(0)
	if got := DefaultTimeout(); got != orig {
		t.Fatalf("SetDefaultTimeout(0) changed the default to %v, want unchanged %v", got, orig)
	}
	SetDefaultTimeout(-1 * time.Second)
	if got := DefaultTimeout(); got != orig {
		t.Fatalf("SetDefaultTimeout(negative) changed the default to %v, want unchanged %v", got, orig)
	}
}

func TestRunWithDir(t *testing.T) {
	dir := t.TempDir()
	stdout, _, exitCode, err := RunWith(Opts{Dir: dir}, "pwd")
	if err != nil {
		t.Fatal(err)
	}
	if exitCode != 0 {
		t.Fatalf("exit %d", exitCode)
	}
	got := strings.TrimSpace(stdout)
	want, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, err = filepath.EvalSymlinks(got)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("pwd = %q, want %q", got, want)
	}
}

func TestRunWithStdin(t *testing.T) {
	tests := []struct {
		name         string
		stdin        string
		cmd          string
		args         []string
		wantStdout   string
		wantExitCode int
		wantErr      bool
	}{
		{
			name:         "stdin-plumbed",
			stdin:        "hello stdin\n",
			cmd:          "cat",
			wantStdout:   "hello stdin\n",
			wantExitCode: 0,
			wantErr:      false,
		},
		{
			name:         "non-zero-exit-is-not-an-error",
			stdin:        "ignored",
			cmd:          "sh",
			args:         []string{"-c", "cat > /dev/null; exit 3"},
			wantExitCode: 3,
			wantErr:      false,
		},
		{
			name:         "fail-binary-not-found",
			stdin:        "ignored",
			cmd:          "non-existent-command-12345",
			wantExitCode: -1,
			wantErr:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stdout, _, exitCode, err := RunWithStdin(tt.stdin, tt.cmd, tt.args...)

			if (err != nil) != tt.wantErr {
				t.Errorf("RunWithStdin() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if exitCode != tt.wantExitCode {
				t.Errorf("RunWithStdin() exitCode = %v, want %v", exitCode, tt.wantExitCode)
			}
			if tt.name == "stdin-plumbed" && stdout != tt.wantStdout {
				t.Errorf("RunWithStdin() stdout = %q, want %q", stdout, tt.wantStdout)
			}
		})
	}
}

// Stdout and stderr must be collected separately even for failing commands.
func TestRunWithStdinCollectsStreams(t *testing.T) {
	stdout, stderr, exitCode, err := RunWithStdin("",
		"sh", "-c", "printf toout; printf toerr >&2; exit 5")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if exitCode != 5 {
		t.Fatalf("exitCode = %d, want 5", exitCode)
	}
	if stdout != "toout" || stderr != "toerr" {
		t.Fatalf("stdout = %q, stderr = %q", stdout, stderr)
	}
}

func TestMergeEnv(t *testing.T) {
	t.Setenv("GONF_MERGE_BASE", "base")
	merged := MergeEnv(map[string]string{
		"GONF_MERGE_BASE": "override",
		"GONF_MERGE_NEW":  "new",
	})

	env := map[string]string{}
	for _, kv := range merged {
		k, v, ok := splitEnv(kv)
		if ok {
			env[k] = v
		}
	}
	if env["GONF_MERGE_BASE"] != "override" {
		t.Fatalf("base = %q", env["GONF_MERGE_BASE"])
	}
	if env["GONF_MERGE_NEW"] != "new" {
		t.Fatalf("new = %q", env["GONF_MERGE_NEW"])
	}
	if _, ok := env["PATH"]; !ok {
		t.Fatal("expected PATH to be preserved")
	}
}

func TestRunWithEnv(t *testing.T) {
	stdout, _, exitCode, err := RunWith(
		Opts{Env: append(os.Environ(), "GONF_RUNWITH=yes")},
		"sh", "-c", "printf '%s' \"$GONF_RUNWITH\"",
	)
	if err != nil {
		t.Fatal(err)
	}
	if exitCode != 0 {
		t.Fatalf("exit %d", exitCode)
	}
	if stdout != "yes" {
		t.Fatalf("stdout = %q", stdout)
	}
}
