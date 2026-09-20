// Package orchestrate fans an already-recorded plan out to a set of
// registered hosts. It is the shared push pipeline behind the public api
// package's PushClusterRun (the whole cluster in one call) and PushFleetRun
// (one call per member cluster's host group).
//
// This is a one-way dependency (api -> internal/orchestrate): this package
// must never import api. It resolves push targets by host name directly via
// internal/inventory (PushTargetFor), so it needs no api.HostRef handle and
// has no dependency on the plan/resource task registry that lives in api —
// callers resolve which hosts and which recorded ops/mem to push; this
// package only executes the fan-out.
package orchestrate

import (
	"context"
	"time"

	"github.com/snonux/gonf/internal/inventory"
	"github.com/snonux/gonf/internal/remote"
	"github.com/snonux/gonf/plan"
)

// Push fans an already-recorded plan (ops/mem, produced by exactly one
// RecordPlanTo call — recording uses package-level global state in the plan
// and resource packages and is not safe to run concurrently or repeatedly
// for one push) out to hostNames via remote.Fanout, bounded by limit
// concurrent per-host pushes. name labels the Fanout summary line and error
// messages (a cluster name for both a whole-cluster push and each of a
// fleet push's per-cluster groups).
//
// This is the single push pipeline shared by every push entry point in api
// (PushClusterRun for the whole cluster in one call; PushFleetRun once per
// member cluster's host group — see PushFleetRun's doc comment for why
// parallelism is applied per group instead of once for the whole fleet).
func Push(ctx context.Context, name, planID string, hostNames []string, limit int, hostTimeout time.Duration, ops []plan.Op, mem plan.BlobReader) error {
	return deliver(ctx, name, planID, hostNames, limit, hostTimeout, ops, mem, false)
}

// Preview performs a strict non-mutating remote preview for every host. It
// shares Push's inventory resolution and fan-out behavior, but remote hosts
// must already have a compatible gonf runtime: no binary bootstrap occurs.
func Preview(ctx context.Context, name, planID string, hostNames []string, limit int, hostTimeout time.Duration, ops []plan.Op, mem plan.BlobReader) error {
	return deliver(ctx, name, planID, hostNames, limit, hostTimeout, ops, mem, true)
}

func deliver(ctx context.Context, name, planID string, hostNames []string, limit int, hostTimeout time.Duration, ops []plan.Op, mem plan.BlobReader, strictPreview bool) error {
	targets := make([]remote.PushTarget, 0, len(hostNames))
	labels := make([]string, 0, len(hostNames))
	for _, hn := range hostNames {
		t, err := inventory.PushTargetFor(hn)
		if err != nil {
			return err
		}
		targets = append(targets, t)
		labels = append(labels, hn)
	}
	if strictPreview {
		return remote.PreviewFanout(ctx, name, planID, ops, mem, targets, labels, limit, hostTimeout)
	}
	return remote.Fanout(ctx, name, planID, ops, mem, targets, labels, limit, hostTimeout)
}
