package api

import (
	"context"
	"time"

	"github.com/snonux/gonf/internal/inventory"
	"github.com/snonux/gonf/internal/remote"
)

func hostPushTarget(h HostRef) (PushTarget, error) {
	return inventory.PushTargetFor(h.Name())
}

// PushHost records and pushes tasks to one HostRef. Thin wrapper: the
// transport half lives in internal/remote (PushTo → remote.Delivery.ToHost).
// It is PushTo with a ForHosts host selection of this host plus every
// inventory name that could match the same machine (aliases, substrings), so
// other cluster members' ForHosts bodies (and their inputs) are skipped.
func PushHost(h HostRef, tasks ...string) error {
	return runHost(remote.Push, h, "push-"+h.Name(), tasks)
}

// PreviewHost performs a strict non-mutating remote preview for one host.
// The host must already have a compatible gonf runtime; no binary bootstrap
// occurs. It uses the same ForHosts host selection as PushHost.
func PreviewHost(h HostRef, tasks ...string) error {
	return runHost(remote.Preview, h, "preview-"+h.Name(), tasks)
}

// PushCluster records once and fans out the same push payload to every host in
// the cluster. Thin wrapper: it runs the full-parameter PushClusterRun with the
// library defaults (no context, no per-run overrides).
func PushCluster(name string, tasks ...string) error {
	return PushClusterRun(context.Background(), name, "", 0, remote.DefaultHostTimeout, tasks...)
}

// PushClusterRun records tasks once and fans the same push out to every host in
// the named cluster. It is the full-parameter form of PushCluster: the CLI
// (gonf cluster) uses it to thread its signal-derived context and per-run
// overrides through — planID ("" → cluster-<name>), parallelOverride (> 0
// overrides the cluster's parallelism), and hostTimeout (per-host push bound;
// <= 0 means unlimited). The per-host transport fan-out itself lives in
// internal/remote (remote.Fanout); the record-once-then-fan-out plumbing is
// shared with PushFleetRun via internal/orchestrate.Deliver.
func PushClusterRun(ctx context.Context, name, planID string, parallelOverride int, hostTimeout time.Duration, tasks ...string) error {
	return groupRun{mode: remote.Push, name: name, planID: planID,
		parallelOverride: parallelOverride, hostTimeout: hostTimeout, tasks: tasks}.cluster(ctx)
}

// PreviewClusterRun records tasks once and performs strict non-mutating
// remote previews across a cluster (planID "" → preview-cluster-<name>).
// Missing or stale remote gonf binaries fail instead of being installed or
// updated.
func PreviewClusterRun(ctx context.Context, name, planID string, parallelOverride int, hostTimeout time.Duration, tasks ...string) error {
	return groupRun{mode: remote.Preview, name: name, planID: planID,
		parallelOverride: parallelOverride, hostTimeout: hostTimeout, tasks: tasks}.cluster(ctx)
}
