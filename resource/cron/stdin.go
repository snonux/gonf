package cron

import (
	"bytes"
	"os/exec"
	"strings"
)

// runCmdWithStdin runs name with args, feeding stdin, returning stdout/stderr/code.
var runCmdWithStdin = func(stdin string, name string, args ...string) (string, string, int, error) {
	cmd := exec.Command(name, args...)
	cmd.Stdin = strings.NewReader(stdin)
	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf
	err := cmd.Run()
	code := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
			err = nil
		} else {
			return stdoutBuf.String(), stderrBuf.String(), -1, err
		}
	}
	return stdoutBuf.String(), stderrBuf.String(), code, nil
}
