package user

import (
	"reflect"
	"testing"

	"github.com/snonux/gonf/resource"
)

// scriptedCall is one expected command and the result the scripted runner
// returns for it.
type scriptedCall struct {
	command string
	args    []string
	stdout  string
	stderr  string
	code    int
	err     error
}

// ensureAs runs b.Ensure under the User resource ID resource/user passes for
// want, so these tests observe the same report IDs as a real apply.
func ensureAs(b Backend, want DesiredUser) error {
	return b.Ensure(resource.FormatID("User", want.Name), want)
}

// scriptedRunner returns a Runner that requires exactly calls, in order, and
// fails the test on any unexpected, reordered, or unconsumed command. Every
// backend test drives its backend through it, so the pinned argv sequences
// are the characterization of each platform's behaviour.
func scriptedRunner(t *testing.T, calls []scriptedCall) Runner {
	t.Helper()
	index := 0
	t.Cleanup(func() {
		if index != len(calls) {
			t.Errorf("ran %d commands, want %d (unconsumed: %v)", index, len(calls), calls[index:])
		}
	})
	return func(command string, args ...string) (string, string, int, error) {
		t.Helper()
		if index == len(calls) {
			t.Fatalf("unexpected command %s %v", command, args)
		}
		want := calls[index]
		index++
		if command != want.command || !reflect.DeepEqual(args, want.args) {
			t.Fatalf("command %s %v, want %s %v", command, args, want.command, want.args)
		}
		return want.stdout, want.stderr, want.code, want.err
	}
}
