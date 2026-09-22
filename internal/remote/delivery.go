package remote

import (
	"context"
	"fmt"

	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
)

const (
	// Push is the compatibility delivery: it installs or upgrades the remote
	// gonf binary when needed (EnsureRemoteGonf), stages blobs, and applies
	// the plan — as a dry run ("apply -n -") when resource.DryRun is set, so
	// "push -n" may still bootstrap gonf.
	Push Mode = iota + 1
	// Preview is the strict, non-mutating preview: it never builds, copies or
	// installs gonf (the remote binary must already be compatible, see
	// RequireRemoteGonf), refuses blob-backed plans because receiving blobs
	// needs remote staging writes, and runs "apply -n -strict-preview -".
	Preview
)

// ensureRuntime is the Push-mode bootstrap step (EnsureRemoteGonf). It is a
// variable only so ObserveBootstrapForTest (and this package's tests) can
// observe which deliveries reach it or make it fail; production code never
// reassigns it.
var ensureRuntime = EnsureRemoteGonf

// rebuildRemoteCmds is buildRemoteCmds as prepareRemote's post-install
// rebuild calls it. Only the binary path differs from the pre-flight build,
// and privilege.WrapApplyBinCmd does not check the path today, so the rebuild
// cannot currently fail once the pre-flight passed; its error branch still
// guards a future path check. It is a variable only so this package's tests
// can drive that branch; production code never reassigns it.
var rebuildRemoteCmds = buildRemoteCmds

// Mode selects how a recorded plan is delivered to a remote host. It is the
// one value that distinguishes a push from a strict preview; it is chosen once
// by the public api entry point (PushTo vs PreviewTo, PushClusterRun vs
// PreviewClusterRun, ...) and carried unchanged, inside a Delivery, through
// internal/orchestrate and Fanout down to each host's chunk stream. It
// replaces a strictPreview bool that used to be threaded through five layers
// as a positional parameter, next to a pair of near-identical functions at
// every layer.
//
// The zero Mode is deliberately invalid: a caller that forgets to set it gets
// an error instead of silently falling back to Push, the mode that may
// install a gonf binary and mutate the host.
type Mode uint8

// Delivery is one recorded plan together with the Mode it is delivered in.
// It is built once, right after recording, and passed by value through every
// layer below the api entry point, so no layer needs its own push/preview
// variant.
type Delivery struct {
	Mode   Mode
	PlanID string
	Ops    []plan.Op
	Mem    plan.BlobReader
}

// String names the mode for diagnostics ("push", "preview").
func (m Mode) String() string {
	switch m {
	case Push:
		return "push"
	case Preview:
		return "preview"
	default:
		return fmt.Sprintf("Mode(%d)", uint8(m))
	}
}

// Verb is the past-tense verb of the per-run summary lines ("pushed %s ...",
// "previewed %s ...").
func (m Mode) Verb() string {
	if m == Preview {
		return "previewed"
	}
	return "pushed"
}

// Validate rejects the zero (and any unknown) Mode. Delivery.ToHost and
// Fanout call it before any SSH traffic; the api entry points call it before
// recording, so an invalid mode neither records the plan (under a plan ID and
// labels that would silently read as a push) nor reaches a host.
func (m Mode) Validate() error {
	if m != Push && m != Preview {
		return fmt.Errorf("remote: invalid delivery mode %s", m)
	}
	return nil
}

// ToHost splits the plan into privilege chunks and streams each chunk to
// one SSH target, in d.Mode. Canceling ctx kills the in-flight ssh. ToHost
// adds no timeout of its own: the single-target api entry points
// (PushToContext, PreviewToContext and the Background-rooted PushTo,
// PreviewTo, PushHost, PreviewHost) pass their caller's ctx and add
// DefaultHostTimeout only when it has no deadline yet, and Fanout passes
// each host its per-host timeout context.
//
// A plan.ValidateChunks pre-flight runs before any SSH traffic: a dep
// recorded in a later privilege chunk (or dangling) fails the delivery
// without sending anything, mirroring the privilege pre-flight.
//
// Push mode: multi-chunk plans with blobs first upload all blobs to a sticky
// dir in a dedicated always-unprivileged session (pushBlobs); every chunk
// then applies plan-only with -apply-dir and no embedded blobs. This keeps
// blob extraction owned by the SSH login user even when the first chunk is
// elevated: root could read the blobs anyway, but the login user could not.
// The sticky dir's path is deterministic per plan ID (Fanout suffixes it per
// host, see forHost) and reused across pushes to the same host. Reuse only
// stays safe because cliApplyStdin (internal/cli) wipes the dir's CONTENTS
// before extracting into it, so ToHost does not care whether the dir was
// empty, fresh, or left over from an interrupted run.
//
// Preview mode: a blob-backed plan is refused before any remote probe, and
// the remote gonf is only verified (every privilege context that will apply
// a chunk), never installed.
func (d Delivery) ToHost(ctx context.Context, t PushTarget) error {
	if err := d.Mode.Validate(); err != nil {
		return err
	}
	chunks := plan.SplitPrivilegeChunks(d.Ops)
	if err := plan.ValidateChunks(chunks); err != nil {
		return err
	}
	hasBlobs := d.Mem != nil && d.Mem.HasBlobs()
	if d.Mode == Preview && hasBlobs {
		return fmt.Errorf("remote preview: plan %q has blobs; strict preview does not stage remote data, use push -n for the compatible dry-run path", d.PlanID)
	}
	// Sticky dir for multi-chunk plans with blobs: uploaded once, referenced
	// read-only by every chunk. A concrete remote path; the ID is sanitized.
	sticky := ""
	if hasBlobs && len(chunks) > 1 {
		sticky = "/tmp/gonf-apply-sticky-" + sanitizeID(d.PlanID)
	}
	t, remotes, err := d.prepareRemote(ctx, t, chunks, sticky)
	if err != nil {
		return err
	}
	return d.stream(ctx, t, chunks, remotes, sticky)
}

// ObserveBootstrapForTest is a test seam: until the returned restore func
// runs, every Push-mode delivery reports its gonf bootstrap step
// (EnsureRemoteGonf) to seen instead of probing or installing anything, as if
// the remote gonf were already current. A Preview-mode delivery never reaches
// that step, which is what tests use this to pin at every layer (Fanout,
// internal/orchestrate, the api entry points). seen may be called
// concurrently by Fanout's goroutines. Like the Assume* seams it swaps
// package state, so tests using it must not run in parallel.
func ObserveBootstrapForTest(seen func(PushTarget)) (restore func()) {
	old := ensureRuntime
	ensureRuntime = func(_ context.Context, t PushTarget) (string, error) {
		seen(t)
		return "", nil
	}
	return func() { ensureRuntime = old }
}

// applyStdinArg is the "gonf apply" argument tail that reads the plan from
// stdin for this mode: Preview always asks for the strict no-staging dry
// run; Push asks for a plain dry run only when resource.DryRun is set
// (push -n) and for a real apply otherwise.
func (m Mode) applyStdinArg() string {
	switch {
	case m == Preview:
		return "-n -strict-preview -"
	case resource.DryRun():
		return "-n -"
	default:
		return "-"
	}
}

// forHost is the Delivery of one fan-out member: the same plan and mode,
// with the plan ID suffixed by the host's label so every host's remote
// staging (e.g. the sticky dir) gets its own name.
func (d Delivery) forHost(label string) Delivery {
	d.PlanID += "-" + label
	return d
}

// prepareRemote builds every chunk's remote apply command and makes sure the
// remote gonf runtime fits d.Mode. The commands are built before any SSH
// traffic, so a privilege misconfiguration (e.g. -privilege=none with an
// elevated chunk) fails before syncing gonf or sending any chunk.
//
// Push installs or upgrades gonf when needed; when that lands the binary at
// a new path, the returned target carries it (GonfPath) and the commands are
// rebuilt so every later remote call — including the sticky blob upload —
// runs the fresh binary. Preview only verifies the remote binary and never
// changes the target.
func (d Delivery) prepareRemote(ctx context.Context, t PushTarget, chunks []plan.Chunk, sticky string) (PushTarget, []string, error) {
	remotes, err := buildRemoteCmds(chunks, t, sticky, d.Mode)
	if err != nil {
		return t, nil, err
	}
	if d.Mode == Preview {
		return t, remotes, requireRemoteGonfForChunks(ctx, t, chunks)
	}
	installed, err := ensureRuntime(ctx, t)
	if err != nil {
		return t, nil, err
	}
	if installed != "" && t.GonfPath == "" {
		t.GonfPath = installed
		remotes, err = rebuildRemoteCmds(chunks, t, sticky, d.Mode)
		if err != nil {
			return t, nil, fmt.Errorf("rebuild remote commands for %s: %w", installed, err)
		}
	}
	return t, remotes, nil
}

// stream uploads the blobs to the sticky dir (when there is one), streams
// every chunk, and best-effort removes the sticky dir afterwards. A
// Preview delivery never has a sticky dir: ToHost refused its blobs.
func (d Delivery) stream(ctx context.Context, t PushTarget, chunks []plan.Chunk, remotes []string, sticky string) error {
	if sticky != "" {
		if err := uploadSticky(ctx, t, chunks, d.Mem, sticky); err != nil {
			return err
		}
	}
	if err := streamChunks(ctx, t, chunks, remotes, d.Mem, sticky); err != nil {
		return err
	}
	if sticky != "" {
		pushRemoveSticky(ctx, t, sticky)
	}
	return nil
}
