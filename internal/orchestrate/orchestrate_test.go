package orchestrate

import (
	"context"
	"io"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/snonux/gonf/internal/inventory"
	"github.com/snonux/gonf/internal/remote"
	"github.com/snonux/gonf/plan"
)

// TestPushUnregisteredHostReturnsError confirms Push resolves each host name
// via internal/inventory itself (no api.HostRef needed) and fails without
// dialing out when a name is not registered.
func TestPushUnregisteredHostReturnsError(t *testing.T) {
	inventory.Reset()
	t.Cleanup(inventory.Reset)

	mem := plan.NewMemoryStore()
	err := Push(context.Background(), "grp", "plan-id", []string{"ghost"}, 1, remote.DefaultHostTimeout, nil, mem)
	if err == nil {
		t.Fatal("Push with an unregistered host: want error, got nil")
	}
	if !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("Push error = %q, want it to name the unregistered host", err.Error())
	}
}

// TestPushFansOutToEachRegisteredHost is a smoke test that Push resolves
// every host name to a target via inventory.PushTargetFor and fans the
// (empty, here) plan out to each one via remote.Fanout -- proving Push has
// everything it needs (inventory + remote) without any api-package
// dependency.
func TestPushFansOutToEachRegisteredHost(t *testing.T) {
	inventory.Reset()
	t.Cleanup(inventory.Reset)
	inventory.AddHost("h1", func(h *inventory.Host) { h.SSHHost = "h1.example" })
	inventory.AddHost("h2", func(h *inventory.Host) { h.SSHHost = "h2.example" })

	oldRunner := remote.SSHRunner
	restoreProbe := remote.AssumeRemotePlanCurrent()
	t.Cleanup(func() {
		remote.SSHRunner = oldRunner
		restoreProbe()
	})
	var calls atomic.Int32
	remote.SSHRunner = func(ctx context.Context, stdin io.Reader, argv []string) error {
		calls.Add(1)
		_, _ = io.Copy(io.Discard, stdin)
		return nil
	}

	mem := plan.NewMemoryStore()
	ops := []plan.Op{{Op: plan.KindEnsureDir, ID: "1", Path: "/tmp/orchestrate-test-dir"}}
	if err := Push(context.Background(), "grp", "plan-id", []string{"h1", "h2"}, 2, remote.DefaultHostTimeout, ops, mem); err != nil {
		t.Fatal(err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("SSHRunner calls = %d, want 2", got)
	}
}
