package options

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/snonux/gonf/resource"
)

// fatalCaseEnv selects, in a re-executed copy of this test binary, the
// fatalCases entry to run. Option misuse aborts through logger.Fatal
// (os.Exit(1)) by the fail-fast DSL contract (docs/plan.md, "Error handling
// contract"), so each case must run in its own process.
const fatalCaseEnv = "GONF_OPTIONS_FATAL_CASE"

// contentOnly is a resource with a single capability, standing in for a real
// resource that lacks the capability an erased option needs.
type contentOnly struct{}

func (*contentOnly) SetContent(string) {}

// watchOnly can arm a change gate but cannot record dependency edges, which
// OnChange needs as well.
type watchOnly struct{}

func (*watchOnly) SetChangeWatch([]string) {}

// fatalCases are option misuses that must abort the recipe, each with a
// fragment of the message the user sees.
var fatalCases = []struct {
	name    string
	run     func()
	wantMsg string
}{
	{"option on resource without capability", func() { WithOwner("x").Apply(struct{}{}) }, "struct {} does not support WithOwner"},
	{"option on partially capable resource", func() { WithValidation("v", []string{CandidatePath}).Apply(&contentOnly{}) }, "*options.contentOnly does not support WithValidation"},
	{"option on nil target", func() { DependsOn(fileA).Apply(nil) }, "<nil> does not support DependsOn"},
	{"erased option through the wrong adapter", func() { ToFileOptions(WithCommand("true"))[0].Apply(&contentOnly{}) }, "does not support WithCommand"},
	{"OnChange without resources", func() { OnChange().Apply(&recorder{}) }, "OnChange requires at least one resource to watch"},
	{"OnChange with an empty Multi", func() { OnChange(resource.Multi(nil)).Apply(&recorder{}) }, "OnChange requires at least one resource to watch"},
	{"OnChange on a target without dependencies", func() { OnChange(fileA).Apply(&watchOnly{}) }, "*options.watchOnly does not support OnChange"},
	{"WatchChanges without ids", func() { WatchChanges().Apply(&recorder{}) }, "WatchChanges requires at least one resource id"},
	{"IfChanged outside daemon-reload", func() { IfChanged.Apply(&watchOnly{}) }, "*options.watchOnly does not support IfChanged"},
	{"WithWatch outside daemon-reload", func() { WithWatch("File[a]").Apply(&watchOnly{}) }, "*options.watchOnly does not support WithWatch"},
	{"empty WithWatch outside daemon-reload", func() { WithWatch().Apply(&watchOnly{}) }, "*options.watchOnly does not support WithWatch"},
	{"NormalizeMode above 0o7777", func() { NormalizeMode(0o10000) }, "outside 0o7777"},
	{"WithMode with a type bit", func() { WithMode(os.ModeDir | 0o755).Apply(&recorder{}) }, "outside 0o7777"},
	{"WithFileMode with a type bit", func() { WithFileMode(os.ModeSymlink | 0o644).Apply(&recorder{}) }, "outside 0o7777"},
}

// TestFatalOptionMisuse re-executes the test binary once per fatalCases entry
// and asserts the child aborted with exit status 1 and the expected message.
// In the child (fatalCaseEnv set) it just runs the selected case; returning
// normally lets the child exit 0, which the parent reports as "did not abort".
func TestFatalOptionMisuse(t *testing.T) {
	if name := os.Getenv(fatalCaseEnv); name != "" {
		runFatalCase(t, name)
		return
	}
	for _, tc := range fatalCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cmd := exec.Command(os.Args[0], "-test.run=^TestFatalOptionMisuse$", "-test.count=1")
			cmd.Env = append(os.Environ(), fatalCaseEnv+"="+tc.name)
			out, err := cmd.CombinedOutput()
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
				t.Fatalf("child err = %v, want exit status 1 (misuse must abort); output:\n%s", err, out)
			}
			if !strings.Contains(string(out), tc.wantMsg) {
				t.Errorf("child output does not contain %q:\n%s", tc.wantMsg, out)
			}
		})
	}
}

// runFatalCase runs the named case inside the child process.
func runFatalCase(t *testing.T, name string) {
	t.Helper()
	for _, tc := range fatalCases {
		if tc.name == name {
			tc.run()
			return
		}
	}
	t.Fatalf("unknown %s %q", fatalCaseEnv, name)
}
