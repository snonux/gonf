package exec

import (
	"bytes"
	"os"
	"os/exec"
)

// Opts configures an optional working directory and environment for RunWith.
// A nil Env means the process inherits the current environment. A non-nil Env
// (including an empty slice) replaces it entirely.
type Opts struct {
	Dir string
	Env []string
}

// Run executes a command with the given arguments and returns stdout, stderr,
// exit code, and any error encountered starting the process.
func Run(name string, args ...string) (stdout, stderr string, exitCode int, err error) {
	return RunWith(Opts{}, name, args...)
}

// RunWith is like Run but applies Dir and Env from opts.
func RunWith(opts Opts, name string, args ...string) (stdout, stderr string, exitCode int, err error) {
	cmd := exec.Command(name, args...)
	if opts.Dir != "" {
		cmd.Dir = opts.Dir
	}
	if opts.Env != nil {
		cmd.Env = opts.Env
	}

	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	err = cmd.Run()

	stdout = stdoutBuf.String()
	stderr = stderrBuf.String()

	if err != nil {
		if exitError, ok := err.(*exec.ExitError); ok {
			exitCode = exitError.ExitCode()
			// The command ran and exited non-zero; surface that via exitCode
			// rather than err so callers can treat start failures separately.
			err = nil
		} else {
			exitCode = -1
			return stdout, stderr, exitCode, err
		}
	} else {
		exitCode = 0
	}

	return stdout, stderr, exitCode, nil
}

// MergeEnv returns a full environment slice: the current process environment
// with each key in extra overriding or appending. Keys in extra with an empty
// value still set that key to "".
func MergeEnv(extra map[string]string) []string {
	if len(extra) == 0 {
		return os.Environ()
	}

	base := os.Environ()
	out := make([]string, 0, len(base)+len(extra))
	seen := make(map[string]struct{}, len(extra))

	for _, kv := range base {
		key, _, ok := splitEnv(kv)
		if !ok {
			out = append(out, kv)
			continue
		}
		if val, override := extra[key]; override {
			out = append(out, key+"="+val)
			seen[key] = struct{}{}
			continue
		}
		out = append(out, kv)
	}

	for key, val := range extra {
		if _, ok := seen[key]; ok {
			continue
		}
		out = append(out, key+"="+val)
	}
	return out
}

func splitEnv(kv string) (key, val string, ok bool) {
	for i := 0; i < len(kv); i++ {
		if kv[i] == '=' {
			return kv[:i], kv[i+1:], true
		}
	}
	return "", "", false
}
