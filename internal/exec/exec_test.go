package exec

import (
	"testing"
)

func TestRun(t *testing.T) {
	tests := []struct {
		name           string
		cmd            string
		args           []string
		wantStdout     string
		wantStderr     string
		wantExitCode   int
		wantErr        bool
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
			wantStderr:   "", // ls stderr varies by OS, but should not be empty usually.
			wantExitCode: 2,  // Typical for ls non-existent
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
			
			// For ls error, we just check that stderr is not empty since exact text varies
			if tt.name == "fail-exit-code" && stderr == "" {
				t.Errorf("Run() stderr = %q, want non-empty", stderr)
			}
		})
	}
}
