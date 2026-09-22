package validator

import (
	"errors"
	"os/exec"
	"strings"
	"testing"
)

// RunInWithheld runs the validator exactly like RunIn but a failure reports
// only the output size; a silent failure and a success add nothing.
func TestRunInWithheldReportsOnlyOutputSize(t *testing.T) {
	const fakeSecret = "fake-secret-in-candidate" // 24 bytes plus echo's newline
	cases := []struct {
		name, script, want string
		wantErr            bool
	}{
		{"echoing failure", "echo " + fakeSecret + "; exit 4",
			": validator output withheld (25 bytes): the candidate holds secret material", true},
		{"silent failure", "exit 4", "exit status 4", true},
		{"echoing success", "echo " + fakeSecret, "", false},
	}
	for _, tc := range cases {
		err := RunInWithheld("", "sh", []string{"-c", tc.script})
		if !tc.wantErr {
			if err != nil {
				t.Errorf("%s: err = %v, want nil", tc.name, err)
			}
			continue
		}
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 4 {
			t.Errorf("%s: err = %v, want the exit status 4 wrapped", tc.name, err)
			continue
		}
		if !strings.HasSuffix(err.Error(), tc.want) || strings.Contains(err.Error(), fakeSecret) {
			t.Errorf("%s: err = %q, want suffix %q and no secret", tc.name, err, tc.want)
		}
	}
	err := RunWithheld("sh", []string{"-c", "echo " + fakeSecret + " >&2; exit 1"})
	if err == nil || strings.Contains(err.Error(), fakeSecret) {
		t.Fatalf("RunWithheld: err = %v, want a failure without the output", err)
	}
}
