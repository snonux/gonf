package api

import (
	"bytes"
	"context"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

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
}

// modeCases lists every exported push/preview pair against
// setupForHostsInventory's inventory ("edge" cluster, "edge-fleet" fleet).
func modeCases() []modeCase {
	ctx := context.Background()
	to := PushTarget{Host: "rex@h2.example"}
	return []modeCase{
		{"PushTo", false, func(ts ...string) error { return PushTo(to, "", ts...) }, "push", "push: no tasks"},
		{"PreviewTo", true, func(ts ...string) error { return PreviewTo(to, "", ts...) }, "push", "remote preview: no tasks"},
		{"PushToContext", false, func(ts ...string) error { return PushToContext(ctx, to, "p", ts...) }, "p", "push: no tasks"},
		{"PreviewToContext", true, func(ts ...string) error { return PreviewToContext(ctx, to, "p", ts...) }, "p", "remote preview: no tasks"},
		{"PushHost", false, func(ts ...string) error { return PushHost(MustHost("h2"), ts...) }, "push-h2", "push: no tasks"},
		{"PreviewHost", true, func(ts ...string) error { return PreviewHost(MustHost("h2"), ts...) }, "preview-h2", "remote preview: no tasks"},
		{"PushCluster", false, func(ts ...string) error { return PushCluster("edge", ts...) }, "cluster-edge", `cluster "edge": no tasks`},
		{"PushClusterRun", false, func(ts ...string) error {
			return PushClusterRun(ctx, "edge", "", 0, remote.DefaultHostTimeout, ts...)
		}, "cluster-edge", `cluster "edge": no tasks`},
		{"PreviewClusterRun", true, func(ts ...string) error {
			return PreviewClusterRun(ctx, "edge", "", 0, remote.DefaultHostTimeout, ts...)
		}, "preview-cluster-edge", `cluster preview "edge": no tasks`},
		{"PushFleet", false, func(ts ...string) error { return PushFleet("edge-fleet", ts...) }, "fleet-edge-fleet", `fleet "edge-fleet": no tasks`},
		{"PushFleetRun", false, func(ts ...string) error {
			return PushFleetRun(ctx, "edge-fleet", "", 0, remote.DefaultHostTimeout, ts...)
		}, "fleet-edge-fleet", `fleet "edge-fleet": no tasks`},
		{"PreviewFleetRun", true, func(ts ...string) error {
			return PreviewFleetRun(ctx, "edge-fleet", "", 0, remote.DefaultHostTimeout, ts...)
		}, "preview-fleet-edge-fleet", `fleet preview "edge-fleet": no tasks`},
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
			if err := tc.run("iter"); err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
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
