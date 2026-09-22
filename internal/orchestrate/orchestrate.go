// Package orchestrate fans an already-recorded plan out to a set of
// registered hosts. It is the shared delivery pipeline behind the public api
// package's cluster runs (PushClusterRun / PreviewClusterRun: the whole
// cluster in one call) and fleet runs (PushFleetRun / PreviewFleetRun: one
// call per member cluster's host group). Push and strict preview share it;
// the remote.Mode inside the remote.Delivery tells them apart.
//
// This is a one-way dependency (api -> internal/orchestrate): this package
// must never import api. It resolves push targets by host name directly via
// internal/inventory (PushTargetFor), so it needs no api.HostRef handle and
// has no dependency on the plan/resource task registry that lives in api —
// callers resolve which hosts and which recorded ops/mem to deliver; this
// package only executes the fan-out.
package orchestrate

import (
	"context"
	"io"
	"time"

	"github.com/snonux/gonf/internal/inventory"
	"github.com/snonux/gonf/internal/remote"
)

// Group names the registered hosts one Deliver call reaches, and how.
type Group struct {
	// Name labels the remote.Fanout summary line and error messages (a
	// cluster name for both a whole-cluster run and each of a fleet run's
	// per-cluster groups).
	Name string
	// HostNames are inventory host names, resolved via
	// inventory.PushTargetFor.
	HostNames []string
	// Limit bounds the concurrent per-host deliveries.
	Limit int
	// HostTimeout bounds each host's whole delivery (<= 0 means unlimited).
	HostTimeout time.Duration
	// Writer receives remote.Fanout's per-group summary line; nil means
	// os.Stderr (see remote.Group.Writer). It is passed straight through to
	// remote.Group so api's cluster and fleet entry points share one output
	// seam with the single-host push/preview path. Deliver writes it once,
	// after g's hosts finish; a caller sharing one Writer between concurrent
	// Deliver calls (api's fleet run) must make it safe for concurrent use.
	Writer io.Writer
}

// Deliver fans an already-recorded plan (d, produced by exactly one
// RecordPlanTo call — recording uses package-level global state in the plan
// and resource packages and is not safe to run concurrently or repeatedly
// for one run) out to g's hosts via remote.Fanout, in d.Mode: remote.Push
// may bootstrap gonf on a host, remote.Preview never does. An unregistered
// host name fails before any SSH traffic. The error is returned as-is (never
// re-formatted), so the per-host causes remote.Fanout keeps stay reachable
// with errors.Is / errors.As.
//
// This is the single pipeline shared by every cluster and fleet entry point
// in api (once for the whole cluster; once per member cluster's host group
// for a fleet — see api.PushFleetRun's doc comment for why parallelism is
// applied per group instead of once for the whole fleet).
func Deliver(ctx context.Context, d remote.Delivery, g Group) error {
	targets := make([]remote.PushTarget, 0, len(g.HostNames))
	for _, hn := range g.HostNames {
		t, err := inventory.PushTargetFor(hn)
		if err != nil {
			return err
		}
		targets = append(targets, t)
	}
	return remote.Fanout(ctx, d, remote.Group{
		Name:        g.Name,
		Targets:     targets,
		Labels:      g.HostNames,
		Limit:       g.Limit,
		HostTimeout: g.HostTimeout,
		Writer:      g.Writer,
	})
}
