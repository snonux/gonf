package exec

import (
	"bytes"
	"os/exec"
)

// Run executes a shell command with the given arguments and returns stdout, stderr, exit code, and any error encountered.
func Run(name string, args ...string) (stdout, stderr string, exitCode int, err error) {
	cmd := exec.Command(name, args...)

	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	err = cmd.Run()

	stdout = stdoutBuf.String()
	stderr = stderrBuf.String()

	if err != nil {
		if exitError, ok := err.(*exec.ExitError); ok {
			exitCode = exitError.ExitCode()
			// In this case, the error is just the non-zero exit code,
			// which we've already captured. We return nil for err to indicate
			// that the command actually ran and exited (even if non-zero).
			err = nil
		} else {
			// This is a "real" error, e.g., binary not found
			exitCode = -1
			return stdout, stderr, exitCode, err
		}
	} else {
		exitCode = 0
	}

	return stdout, stderr, exitCode, nil
}
