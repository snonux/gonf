package api

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/internal/remote"
	"github.com/snonux/gonf/plan"
)

// modeRecorder fakes SSH and every remote gonf probe, and observes the
// Push-mode gonf bootstrap step (remote.ObserveBootstrapForTest).
type modeRecorder struct {
	bootstraps atomic.Int32
	mu         sync.Mutex
	remoteCmds []string
	stdins     []string
}

func installModeRecorder(t *testing.T) *modeRecorder {
	t.Helper()
	r := &modeRecorder{}
	old := remote.SSHRunner
	restoreProbes := remote.AssumeRemoteGonfCurrent()
	restoreBootstrap := remote.ObserveBootstrapForTest(func(remote.PushTarget) { r.bootstraps.Add(1) })
	t.Cleanup(func() {
		remote.SSHRunner = old
		restoreBootstrap()
		restoreProbes()
	})
	remote.SSHRunner = func(_ context.Context, stdin io.Reader, argv []string) error {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, stdin)
		r.mu.Lock()
		defer r.mu.Unlock()
		r.remoteCmds = append(r.remoteCmds, argv[len(argv)-1])
		r.stdins = append(r.stdins, buf.String())
		return nil
	}
	return r
}

// modeCase is one exported push or preview entry point, the default plan ID
// it must record, and the error its call without tasks must return.
type modeCase struct {
	name      string
	preview   bool
	run       func(tasks ...string) error
	planID    string
	noTaskErr string
	// summary matches every stderr summary line the run prints.
	summary string
	// failPrefix starts the error when every SSH session fails ("" for the
	// single-target entry points, whose error is the bare chunk failure).
	failPrefix string
}

// modeCases lists every exported push/preview pair against
// setupForHostsInventory's inventory ("edge" cluster, "edge-fleet" fleet).
func modeCases() []modeCase {
	ctx := context.Background()
	to := PushTarget{Host: "rex@h2.example"}
	const (
		toSummary      = `^pushed %s \(\d+ ops\) to rex@h2\.example$`
		previewSummary = `^previewed %s \(\d+ ops\) on rex@h2\.example$`
		groupSummary   = `^%s %s \(\d+ ops\) to edge \(2/2 hosts\)$`
	)
	fleetFail := `fleet "edge-fleet": cluster "edge": `
	return []modeCase{
		{"PushTo", false, func(ts ...string) error { return PushTo(to, "", ts...) }, "push", "push: no tasks",
			fmt.Sprintf(toSummary, "push"), ""},
		{"PreviewTo", true, func(ts ...string) error { return PreviewTo(to, "", ts...) }, "push", "remote preview: no tasks",
			fmt.Sprintf(previewSummary, "push"), ""},
		{"PushToContext", false, func(ts ...string) error { return PushToContext(ctx, to, "p", ts...) }, "p", "push: no tasks",
			fmt.Sprintf(toSummary, "p"), ""},
		{"PreviewToContext", true, func(ts ...string) error { return PreviewToContext(ctx, to, "p", ts...) }, "p", "remote preview: no tasks",
			fmt.Sprintf(previewSummary, "p"), ""},
		{"PushHost", false, func(ts ...string) error { return PushHost(MustHost("h2"), ts...) }, "push-h2", "push: no tasks",
			fmt.Sprintf(toSummary, "push-h2"), ""},
		{"PreviewHost", true, func(ts ...string) error { return PreviewHost(MustHost("h2"), ts...) }, "preview-h2", "remote preview: no tasks",
			fmt.Sprintf(previewSummary, "preview-h2"), ""},
		{"PushCluster", false, func(ts ...string) error { return PushCluster("edge", ts...) }, "cluster-edge", `cluster "edge": no tasks`,
			fmt.Sprintf(groupSummary, "pushed", "cluster-edge"), `cluster "edge": `},
		{"PushClusterRun", false, func(ts ...string) error {
			return PushClusterRun(ctx, "edge", "", 0, remote.DefaultHostTimeout, ts...)
		}, "cluster-edge", `cluster "edge": no tasks`,
			fmt.Sprintf(groupSummary, "pushed", "cluster-edge"), `cluster "edge": `},
		{"PreviewClusterRun", true, func(ts ...string) error {
			return PreviewClusterRun(ctx, "edge", "", 0, remote.DefaultHostTimeout, ts...)
		}, "preview-cluster-edge", `cluster preview "edge": no tasks`,
			fmt.Sprintf(groupSummary, "previewed", "preview-cluster-edge"), `cluster "edge": `},
		{"PushFleet", false, func(ts ...string) error { return PushFleet("edge-fleet", ts...) }, "fleet-edge-fleet", `fleet "edge-fleet": no tasks`,
			fmt.Sprintf(groupSummary, "pushed", "fleet-edge-fleet"), fleetFail},
		{"PushFleetRun", false, func(ts ...string) error {
			return PushFleetRun(ctx, "edge-fleet", "", 0, remote.DefaultHostTimeout, ts...)
		}, "fleet-edge-fleet", `fleet "edge-fleet": no tasks`,
			fmt.Sprintf(groupSummary, "pushed", "fleet-edge-fleet"), fleetFail},
		{"PreviewFleetRun", true, func(ts ...string) error {
			return PreviewFleetRun(ctx, "edge-fleet", "", 0, remote.DefaultHostTimeout, ts...)
		}, "preview-fleet-edge-fleet", `fleet preview "edge-fleet": no tasks`,
			fmt.Sprintf(groupSummary, "previewed", "preview-fleet-edge-fleet"), fleetFail},
	}
}

// TestEntryPointsPinDeliveryMode pins, for every exported push/preview entry
// point, the remote.Mode it hands down: a push reaches the gonf bootstrap
// step on every host and runs a plain remote apply; a preview never
// bootstraps and runs the strict remote preview. The default plan ID it
// records is pinned too.
func TestEntryPointsPinDeliveryMode(t *testing.T) {
	for _, tc := range modeCases() {
		t.Run(tc.name, func(t *testing.T) {
			setupForHostsInventory(t)
			registerForHostsTask("iter", "all")
			r := installModeRecorder(t)
			var err error
			stderr := captureStderr(t, func() { err = tc.run("iter") })
			if err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			checkSummary(t, tc, stderr)
			wantCmd, bootstrapped := "gonf apply -", r.bootstraps.Load() > 0
			if tc.preview {
				wantCmd = "gonf apply -n -strict-preview -"
			}
			if bootstrapped == tc.preview {
				t.Fatalf("%s: bootstraps = %d, preview = %v", tc.name, r.bootstraps.Load(), tc.preview)
			}
			if len(r.remoteCmds) == 0 {
				t.Fatalf("%s opened no SSH connection", tc.name)
			}
			for i, c := range r.remoteCmds {
				if c != wantCmd {
					t.Fatalf("%s: remote cmd = %q, want %q", tc.name, c, wantCmd)
				}
				payload, err := plan.DecodePush(strings.NewReader(r.stdins[i]), "")
				if err != nil {
					t.Fatalf("%s: decode: %v", tc.name, err)
				}
				if got := payload.Ops[0].ID; got != tc.planID {
					t.Fatalf("%s: plan ID = %q, want %q", tc.name, got, tc.planID)
				}
			}
		})
	}
}

// TestEntryPointsKeepNoTasksErrors pins each entry point's "no tasks" error
// text, which names the mode (push vs preview) and scope; nothing reaches
// SSH.
func TestEntryPointsKeepNoTasksErrors(t *testing.T) {
	for _, tc := range modeCases() {
		t.Run(tc.name, func(t *testing.T) {
			setupForHostsInventory(t)
			r := installModeRecorder(t)
			err := tc.run()
			if err == nil || err.Error() != tc.noTaskErr {
				t.Fatalf("%s() = %v, want %q", tc.name, err, tc.noTaskErr)
			}
			if len(r.remoteCmds) != 0 || r.bootstraps.Load() != 0 {
				t.Fatalf("%s without tasks reached the remote", tc.name)
			}
		})
	}
}

// TestEntryPointsKeepOpaqueRefusalLabel pins the label each entry point puts
// in front of the opaque-When refusal: the same "<scope>: " prefix as its
// "no tasks" error ("push", "remote preview", `cluster preview "edge"`, ...).
// The refusal comes after recording and before any SSH traffic.
func TestEntryPointsKeepOpaqueRefusalLabel(t *testing.T) {
	for _, tc := range modeCases() {
		t.Run(tc.name, func(t *testing.T) {
			setupForHostsInventory(t)
			Task("opaque", "", func() {
				File("/tmp/opaque-refusal", options.WithContent("x\n"))
			}, When(func(Facts) bool { return true }))
			r := installModeRecorder(t)
			err := tc.run("opaque")
			label := strings.TrimSuffix(tc.noTaskErr, "no tasks")
			if err == nil || !strings.HasPrefix(err.Error(), label+"task(s) opaque use When(func)") {
				t.Fatalf("%s() = %v, want the opaque refusal labelled %q", tc.name, err, label)
			}
			if len(r.remoteCmds) != 0 || r.bootstraps.Load() != 0 {
				t.Fatalf("%s refused plan reached the remote", tc.name)
			}
		})
	}
}

// TestEntryPointsKeepFailurePrefix pins the error prefix when every SSH
// session fails. Cluster runs report `cluster "edge": ` in both modes, and
// fleet runs `fleet "edge-fleet": cluster "edge": ` — a fleet preview keeps
// the plain `fleet "<name>"` prefix, not the `fleet preview` no-tasks label.
func TestEntryPointsKeepFailurePrefix(t *testing.T) {
	for _, tc := range modeCases() {
		t.Run(tc.name, func(t *testing.T) {
			setupForHostsInventory(t)
			registerForHostsTask("iter", "all")
			installModeRecorder(t)
			remote.SSHRunner = func(_ context.Context, stdin io.Reader, _ []string) error {
				_, _ = io.Copy(io.Discard, stdin)
				return errors.New("boom")
			}
			var err error
			captureStderr(t, func() { err = tc.run("iter") })
			if err == nil {
				t.Fatalf("%s() succeeded, want the ssh failure", tc.name)
			}
			// The prefix is followed directly by a host label (h1-wg, h2).
			if tc.failPrefix != "" && !strings.HasPrefix(err.Error(), tc.failPrefix+"h") {
				t.Fatalf("%s() = %v, want prefix %q", tc.name, err, tc.failPrefix)
			}
			if !strings.Contains(err.Error(), "boom") {
				t.Fatalf("%s() = %v, want the ssh failure", tc.name, err)
			}
		})
	}
}

// checkSummary asserts the run printed at least one summary line and that
// every "pushed"/"previewed" line matches tc.summary ("to" vs "on" included).
func checkSummary(t *testing.T, tc modeCase, stderr string) {
	t.Helper()
	re := regexp.MustCompile(tc.summary)
	lines := 0
	for _, line := range strings.Split(stderr, "\n") {
		if !strings.HasPrefix(line, "pushed ") && !strings.HasPrefix(line, "previewed ") {
			continue
		}
		lines++
		if !re.MatchString(line) {
			t.Fatalf("%s: summary %q does not match %s", tc.name, line, tc.summary)
		}
	}
	if lines == 0 {
		t.Fatalf("%s: no summary line in stderr %q", tc.name, stderr)
	}
}

// captureStderr runs fn with os.Stderr redirected to a pipe and returns what
// was written: the summary lines under test are printed to os.Stderr
// directly. Tests using it must not run in parallel.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = w
	done := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	defer func() { os.Stderr = old }()
	fn()
	os.Stderr = old
	_ = w.Close()
	return <-done
}
