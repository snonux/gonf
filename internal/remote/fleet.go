package remote

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/snonux/gonf/plan"
)

// DefaultHostTimeout bounds one host's whole push (all chunks: blob upload,
// applies, sticky removal). Applies can legitimately run for minutes, so the
// default is generous; `gonf fleet -host-timeout` overrides it per run. A
// host that outlives its limit is killed and reported as a fleet failure
// instead of holding an errgroup slot forever.
const DefaultHostTimeout = 10 * time.Minute

// hostTimeoutCtx derives the per-host push context: the fleet context bounded
// by hostTimeout when one is configured (hostTimeout <= 0 means unlimited).
func hostTimeoutCtx(fleetCtx context.Context, hostTimeout time.Duration) (context.Context, context.CancelFunc) {
	if hostTimeout > 0 {
		return context.WithTimeout(fleetCtx, hostTimeout)
	}
	return context.WithCancel(fleetCtx)
}

// Fanout pushes one already-recorded plan to every target in parallel. It is
// the per-host transport half of the fleet push: the caller (api.PushClusterRun)
// resolves the fleet inventory and records the plan once, then hands the
// resolved targets here. name labels the summary line and error messages;
// labels[i] names targets[i]. The ctx (the CLI signal context for the fleet
// fan-out) is thread through errgroup.WithContext: SIGINT/SIGTERM kill the
// in-flight ssh pushes, and a failing host cancels its in-flight siblings.
// Each host's push is bounded by hostTimeout (DefaultHostTimeout for library
// callers, the CLI -host-timeout flag otherwise; <= 0 means unlimited).
func Fanout(ctx context.Context, name, planID string, ops []plan.Op, mem plan.BlobReader, targets []PushTarget, labels []string, limit int, hostTimeout time.Duration) error {
	return fanout(ctx, name, planID, ops, mem, targets, labels, limit, hostTimeout, false)
}

// PreviewFanout is Fanout's strict-preview variant. Each target must already
// have a compatible gonf runtime; unlike Fanout it never bootstraps it.
func PreviewFanout(ctx context.Context, name, planID string, ops []plan.Op, mem plan.BlobReader, targets []PushTarget, labels []string, limit int, hostTimeout time.Duration) error {
	return fanout(ctx, name, planID, ops, mem, targets, labels, limit, hostTimeout, true)
}

func fanout(ctx context.Context, name, planID string, ops []plan.Op, mem plan.BlobReader, targets []PushTarget, labels []string, limit int, hostTimeout time.Duration, strictPreview bool) error {
	var (
		eg       *errgroup.Group
		egCtx    context.Context
		errMu    sync.Mutex
		failed   []string
		okCount  int
		firstErr error
	)
	// WithContext ties the fan-out to ctx (the CLI signal context) and makes
	// a failing host cancel its in-flight siblings: their ssh processes are
	// killed instead of holding errgroup slots forever.
	eg, egCtx = errgroup.WithContext(ctx)
	eg.SetLimit(limit)
	for i := range targets {
		i := i
		eg.Go(func() error {
			hostCtx, cancel := hostTimeoutCtx(egCtx, hostTimeout)
			defer cancel()
			var err error
			if strictPreview {
				err = PreviewChunks(hostCtx, targets[i], planID+"-"+labels[i], ops, mem)
			} else {
				err = PushChunks(hostCtx, targets[i], planID+"-"+labels[i], ops, mem)
			}
			errMu.Lock()
			defer errMu.Unlock()
			if err == nil {
				okCount++
				return nil
			}
			switch {
			case errors.Is(err, context.Canceled):
				// Killed by a fleet-wide abort (a sibling host failed or the
				// CLI context fired), not by this host's own failure: record
				// the abort reason once instead of blaming every in-flight
				// host.
				if firstErr == nil {
					firstErr = err
				}
			case errors.Is(err, context.DeadlineExceeded):
				failed = append(failed, fmt.Sprintf("%s: %v (host timeout after %s)", labels[i], err, hostTimeout))
			default:
				failed = append(failed, fmt.Sprintf("%s: %v", labels[i], err))
			}
			return err
		})
	}
	// Every goroutine error is collected above (failed[] per host, firstErr
	// for aborts); Wait's own first error would be redundant.
	_ = eg.Wait()

	verb := "pushed"
	if strictPreview {
		verb = "previewed"
	}
	fmt.Fprintf(os.Stderr, "%s %s (%d ops) to %s (%d/%d hosts)\n",
		verb, planID, len(ops), name, okCount, len(targets))
	if len(failed) > 0 {
		sort.Strings(failed)
		return fmt.Errorf("cluster %q: %s", name, strings.Join(failed, "; "))
	}
	if firstErr != nil {
		return fmt.Errorf("cluster %q: aborted: %w", name, firstErr)
	}
	return nil
}
