package api

import (
	"context"
	"fmt"
	"os"

	"github.com/snonux/gonf/internal/privilege"
	"github.com/snonux/gonf/internal/remote"
	"github.com/snonux/gonf/plan"
)

// PushTarget is one SSH destination (inventory optional). It is an alias for
// remote.PushTarget: the type itself lives in the internal/remote transport
// package (ssh argv building, push protocol), and the alias keeps the public
// PushTo/PushPayload signatures stable for library users.
type PushTarget = remote.PushTarget

// processPrivilege is the CLI/local default for wrapping privileged chunks.
var processPrivilege = privilege.None

// SetPrivilege sets the process-wide privilege helper (CLI -privilege).
func SetPrivilege(mode privilege.Mode) { processPrivilege = mode }

// Privilege returns the process-wide privilege helper.
func Privilege() privilege.Mode { return processPrivilege }

// PushPayload streams an already-encoded GONF-PUSH/1 blob to one SSH target.
// Thin wrapper: the implementation (ssh transport) lives in internal/remote.
// Kept context.Background()-rooted (equivalent to
// PushPayloadContext(context.Background(), ...)) for API stability; new
// callers that can supply a context should use PushPayloadContext instead.
func PushPayload(t PushTarget, payload []byte, elevate bool, applyDir string) error {
	return remote.PushPayloadContext(context.Background(), t, payload, elevate, applyDir)
}

// PushPayloadContext is PushPayload bounded/cancelable by ctx: when ctx has
// no deadline of its own, remote.DefaultHostTimeout is applied so a wedged
// remote command cannot hang the push forever (mirroring the per-host
// timeout the fleet fan-out already applies to remote.PushChunks).
func PushPayloadContext(ctx context.Context, t PushTarget, payload []byte, elevate bool, applyDir string) error {
	return remote.PushPayloadContext(ctx, t, payload, elevate, applyDir)
}

// PushTo records tasks, splits privilege chunks, and streams each chunk over
// SSH. Kept context.Background()-rooted (equivalent to
// PushToContext(context.Background(), ...)) for API stability — existing
// callers (including client repos that call PushTo directly) keep their
// current signature and behavior unchanged. PushToContext still bounds the
// push with remote.DefaultHostTimeout when the given ctx has no deadline, so
// even this "uncancelable" entry point cannot hang forever; only a
// caller-driven cancellation (SIGINT via the CLI's signal context) requires
// the *Context variant. The recording half lives here (api task registry);
// the chunk streaming lives in remote.PushChunks.
func PushTo(t PushTarget, planID string, tasks ...string) error {
	return PushToContext(context.Background(), t, planID, tasks...)
}

// PushToContext is PushTo bounded/cancelable by ctx: canceling ctx (e.g. the
// CLI's SIGINT/SIGTERM context) kills the in-flight ssh push. When ctx has no
// deadline of its own, remote.DefaultHostTimeout is applied.
func PushToContext(ctx context.Context, t PushTarget, planID string, tasks ...string) error {
	if len(tasks) == 0 {
		return fmt.Errorf("push: no tasks")
	}
	if planID == "" {
		planID = "push"
	}
	mem := plan.NewMemoryStore()
	ops, err := RecordPlanTo(planID, mem, tasks...)
	if err != nil {
		return fmt.Errorf("record: %w", err)
	}
	if err := RefuseOpaqueOnlyPush("push"); err != nil {
		return err
	}
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, remote.DefaultHostTimeout)
		defer cancel()
	}
	if err := remote.PushChunks(ctx, t, planID, ops, mem); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "pushed %s (%d ops) to %s\n", planID, len(ops), t.Destination())
	return nil
}
