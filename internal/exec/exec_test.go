package exec

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
