package api

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/snonux/gonf/resource"
)

func cliHosts() int {
	infos := Hosts()
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

func cliFleets() int {
	infos := Fleets()
	if len(infos) == 0 {
		fmt.Fprintln(os.Stderr, "no fleets registered")
		return 1
	}
	for _, f := range infos {
		fmt.Printf("%s\tj=%d\t%s\n", f.Name, f.Parallelism, strings.Join(f.Hosts, ","))
	}
	return 0
}

func cliFleet(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("fleet", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	dryRun := fs.Bool("dry-run", false, "Remote dry-run (-n on apply)")
	dryRunShort := fs.Bool("n", false, "Alias for -dry-run")
	planID := fs.String("id", "", "plan id written into the header (default fleet-<name>)")
	jobs := fs.Int("j", 0, "override fleet parallelism for this run")
	hostTimeout := fs.Duration("host-timeout", defaultHostTimeout, "per-host push timeout (all chunks; 0 = unlimited)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	pos := fs.Args()
	if len(pos) < 2 {
		fmt.Fprintln(os.Stderr, "usage: gonf fleet [-n|-dry-run] [-j N] [-id name] [-host-timeout 10m] <fleet> <task> [task...]")
		return 2
	}
	if *dryRun || *dryRunShort {
		resource.SetDryRun(true)
	}
	name := pos[0]
	tasks := pos[1:]
	id := *planID
	if id == "" {
		id = "fleet-" + name
	}
	if err := pushFleet(ctx, name, id, *jobs, *hostTimeout, tasks...); err != nil {
		fmt.Fprintf(os.Stderr, "fleet: %v\n", err)
		return 1
	}
	return 0
}
