package cli

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/snonux/gonf/api"
	"github.com/snonux/gonf/internal/remote"
	"github.com/snonux/gonf/resource"
)

func cliHosts() int {
	infos := api.Hosts()
	if len(infos) == 0 {
		fmt.Fprintln(os.Stderr, "no hosts registered")
		return 1
	}
	for _, h := range infos {
		dest := h.SSHHost
		if h.User != "" {
			dest = h.User + "@" + h.SSHHost
		}
		extra := ""
		if h.Port > 0 {
			extra += fmt.Sprintf(" port=%d", h.Port)
		}
		if h.Identity != "" {
			extra += " identity=" + h.Identity
		}
		fmt.Printf("%s\t%s%s\n", h.Name, dest, extra)
	}
	return 0
}

func cliClusters() int {
	infos := api.Clusters()
	if len(infos) == 0 {
		fmt.Fprintln(os.Stderr, "no clusters registered")
		return 1
	}
	for _, f := range infos {
		fmt.Printf("%s\tj=%d\t%s\n", f.Name, f.Parallelism, strings.Join(f.Hosts, ","))
	}
	return 0
}

func cliFleets() int {
	infos := api.Fleets()
	if len(infos) == 0 {
		fmt.Fprintln(os.Stderr, "no fleets registered")
		return 1
	}
	for _, f := range infos {
		fmt.Printf("%s\tclusters=%s\thosts=%s\n", f.Name, strings.Join(f.Clusters, ","), strings.Join(f.Hosts, ","))
	}
	return 0
}

// pushFlags holds the flags shared by `gonf cluster` and `gonf fleet` — the
// two subcommands take an identical flag set (-n/-dry-run, -id, -j,
// -host-timeout) followed by <name> <task> [task...], and only differ in
// which api.Push*Run function they call and in the "cluster"/"fleet" word
// used for their usage text and default plan id. parsePushFlags is the one
// shared parser; cliCluster/cliFleet used to each carry their own
// near-identical copy of it, including the plan-ID-default calculation.
type pushFlags struct {
	dryRun        bool
	strictPreview bool
	planID        string
	jobs          int
	hostTimeout   time.Duration
	name          string
	tasks         []string
}

// parsePushFlags parses the `gonf cluster`/`gonf fleet` flag set. kind is
// "cluster" or "fleet" (used for the flagset name, -id default, and usage
// text). ok is false when parsing failed or usage was printed; the caller
// should return exitCode immediately in that case.
func parsePushFlags(kind string, args []string) (pf pushFlags, exitCode int, ok bool) {
	fs := flag.NewFlagSet(kind, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	dryRun := fs.Bool("dry-run", false, "Remote dry-run (-n on apply)")
	dryRunShort := fs.Bool("n", false, "Alias for -dry-run")
	preview := fs.Bool("preview", false, "Strict remote preview: no gonf bootstrap or remote writes")
	planID := fs.String("id", "", fmt.Sprintf("plan id written into the header (default %s-<name>)", kind))
	jobs := fs.Int("j", 0, fmt.Sprintf("override %s fan-out parallelism for this run", kind))
	hostTimeout := fs.Duration("host-timeout", remote.DefaultHostTimeout, "per-host push timeout (all chunks; 0 = unlimited)")
	if err := fs.Parse(args); err != nil {
		return pushFlags{}, 2, false
	}
	pos := fs.Args()
	if len(pos) < 2 {
		fmt.Fprintf(os.Stderr, "usage: gonf %s [-n|-dry-run|-preview] [-j N] [-id name] [-host-timeout 10m] <%s> <task> [task...]\n", kind, kind)
		return pushFlags{}, 2, false
	}
	name := pos[0]
	id := *planID
	if id == "" {
		id = kind + "-" + name
	}
	return pushFlags{
		dryRun:        *dryRun || *dryRunShort,
		strictPreview: *preview,
		planID:        id,
		jobs:          *jobs,
		hostTimeout:   *hostTimeout,
		name:          name,
		tasks:         pos[1:],
	}, 0, true
}

func cliFleet(ctx context.Context, args []string) int {
	pf, exitCode, ok := parsePushFlags("fleet", args)
	if !ok {
		return exitCode
	}
	// Escalate-only: a top-level "gonf -n fleet ..." already set this via
	// CLI()'s unconditional call before dispatch; don't stomp it back to
	// false just because this subcommand's own flags didn't repeat -n.
	if pf.dryRun {
		resource.SetDryRun(true)
	}
	var err error
	if pf.strictPreview {
		err = api.PreviewFleetRun(ctx, pf.name, pf.planID, pf.jobs, pf.hostTimeout, pf.tasks...)
	} else {
		err = api.PushFleetRun(ctx, pf.name, pf.planID, pf.jobs, pf.hostTimeout, pf.tasks...)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "fleet: %v\n", err)
		return 1
	}
	return 0
}

func cliCluster(ctx context.Context, args []string) int {
	pf, exitCode, ok := parsePushFlags("cluster", args)
	if !ok {
		return exitCode
	}
	// Escalate-only: see cliFleet.
	if pf.dryRun {
		resource.SetDryRun(true)
	}
	var err error
	if pf.strictPreview {
		err = api.PreviewClusterRun(ctx, pf.name, pf.planID, pf.jobs, pf.hostTimeout, pf.tasks...)
	} else {
		err = api.PushClusterRun(ctx, pf.name, pf.planID, pf.jobs, pf.hostTimeout, pf.tasks...)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "cluster: %v\n", err)
		return 1
	}
	return 0
}
