package api

import (
	"context"
	"fmt"
	"os"

	"github.com/snonux/gonf/internal/inventory"
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
// The payload is one chunk already encoded by the caller, so no
// dangling-dependency pre-flight runs here (see ApplyPlan); PushTo, which
// records the whole plan, does run it.
func PushPayload(t PushTarget, payload []byte, elevate bool, applyDir string) error {
	return remote.PushPayloadContext(context.Background(), t, payload, elevate, applyDir)
}

// PushPayloadContext is PushPayload bounded/cancelable by ctx: when ctx has
// no deadline of its own, remote.DefaultHostTimeout is applied so a wedged
// remote command cannot hang the push forever (mirroring the per-host
// timeout the fleet fan-out already applies to remote.Delivery.ToHost).
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
// the chunk streaming lives in remote.Delivery.ToHost (Push mode).
func PushTo(t PushTarget, planID string, tasks ...string) error {
	return PushToContext(context.Background(), t, planID, tasks...)
}

// PushToContext is PushTo bounded/cancelable by ctx: canceling ctx (e.g. the
// CLI's SIGINT/SIGTERM context) kills the in-flight ssh push. When ctx has no
// deadline of its own, remote.DefaultHostTimeout is applied.
func PushToContext(ctx context.Context, t PushTarget, planID string, tasks ...string) error {
	return recordAndPush(ctx, remote.Push, t, planID, destinationHosts(t), tasks...)
}

// PreviewTo records tasks and performs a strict non-mutating remote preview.
// The remote host must already have a gonf release and plan schema at least as
// new as the controller; PreviewTo never installs or updates it. Existing
// PushTo callers retain the compatibility behavior that bootstraps gonf when
// needed, including when resource dry-run is enabled.
func PreviewTo(t PushTarget, planID string, tasks ...string) error {
	return PreviewToContext(context.Background(), t, planID, tasks...)
}

// PreviewToContext is PreviewTo bounded and cancelable by ctx. It invokes the
// remote strict-preview apply mode, which rejects blob-backed plans instead
// of staging remote data and runs resource probes under dry-run semantics.
func PreviewToContext(ctx context.Context, t PushTarget, planID string, tasks ...string) error {
	return recordAndPush(ctx, remote.Preview, t, planID, destinationHosts(t), tasks...)
}

// destinationHosts maps a raw push target (e.g. `gonf push user@host`) to the
// ForHosts record-time host selection. Only an exact, unambiguous inventory
// destination without raw ssh arguments narrows; anything else yields nil and
// records every host, exactly as before host selection existed (see
// inventory.SelectionForDestination).
func destinationHosts(t PushTarget) []string {
	return inventory.SelectionForDestination(t.User, t.Host, t.Port, len(t.ExtraSSH) > 0)
}

// recordAndPush records tasks with selected as the ForHosts host selection
// (nil → every host), refuses opaque-only plans, and delivers the chunks to t
// in mode: remote.Push may bootstrap gonf on t, remote.Preview never does.
// It is the single implementation behind PushTo/PreviewTo (and their
// *Context forms) and PushHost/PreviewHost; the exported pairs only choose
// the mode, the plan ID and the host selection.
func recordAndPush(ctx context.Context, mode remote.Mode, t PushTarget, planID string, selected []string, tasks ...string) error {
	label := targetLabel(mode)
	if len(tasks) == 0 {
		return fmt.Errorf("%s: no tasks", label)
	}
	if planID == "" {
		planID = "push"
	}
	d, err := recordDelivery(mode, planID, label, selected, tasks)
	if err != nil {
		return err
	}
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, remote.DefaultHostTimeout)
		defer cancel()
	}
	if err := d.ToHost(ctx, t); err != nil {
		return err
	}
	// "pushed ... to host" but "previewed ... on host": the wording predates
	// Mode and is kept byte-identical for anyone reading stderr.
	preposition := "to"
	if mode == remote.Preview {
		preposition = "on"
	}
	fmt.Fprintf(os.Stderr, "%s %s (%d ops) %s %s\n", mode.Verb(), planID, len(d.Ops), preposition, t.Destination())
	return nil
}

// recordDelivery records tasks once with selected as the ForHosts host
// selection, refuses an opaque-only plan (label prefixes that refusal), and
// returns the recorded plan as a remote.Delivery in mode. Every push and
// preview entry point (single target, cluster, fleet) records through it, so
// the Delivery — and with it the mode — is built exactly once per run and
// then carried unchanged down to each host.
func recordDelivery(mode remote.Mode, planID, label string, selected, tasks []string) (remote.Delivery, error) {
	mem := plan.NewMemoryStore()
	ops, err := recordPlanForHosts(selected, planID, mem, tasks...)
	if err != nil {
		return remote.Delivery{}, fmt.Errorf("record: %w", err)
	}
	if err := RefuseOpaqueOnlyPush(label); err != nil {
		return remote.Delivery{}, err
	}
	return remote.Delivery{Mode: mode, PlanID: planID, Ops: ops, Mem: mem}, nil
}

// targetLabel is a single-target run's error prefix: "push" or
// "remote preview".
func targetLabel(mode remote.Mode) string {
	if mode == remote.Preview {
		return "remote preview"
	}
	return "push"
}

// groupLabel is a cluster or fleet run's error prefix: `cluster "web"` for a
// push, `cluster preview "web"` for a strict preview (scope is "cluster" or
// "fleet").
func groupLabel(mode remote.Mode, scope, name string) string {
	if mode == remote.Preview {
		return fmt.Sprintf("%s preview %q", scope, name)
	}
	return fmt.Sprintf("%s %q", scope, name)
}

// groupPlanID is a cluster or fleet run's default plan ID: "cluster-web" for
// a push, "preview-cluster-web" for a strict preview.
func groupPlanID(mode remote.Mode, scope, name string) string {
	if mode == remote.Preview {
		return "preview-" + scope + "-" + name
	}
	return scope + "-" + name
}
