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
func PushPayload(t PushTarget, payload []byte, elevate bool, applyDir string) error {
	return remote.PushPayload(t, payload, elevate, applyDir)
}

// PushTo records tasks, splits privilege chunks, and streams each chunk over
// SSH. Single-target pushes run without a controller-managed context (only
// the fleet fan-out is signal-cancellable); the ssh handshake is still
// bounded by the generated ConnectTimeout. The recording half lives here
// (api task registry); the chunk streaming lives in remote.PushChunks.
func PushTo(t PushTarget, planID string, tasks ...string) error {
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
	if err := remote.PushChunks(context.Background(), t, planID, ops, mem); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "pushed %s (%d ops) to %s\n", planID, len(ops), t.Destination())
	return nil
}
