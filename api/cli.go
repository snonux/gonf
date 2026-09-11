package api

import (
	"flag"
	"fmt"
	"os"

	"github.com/snonux/gonf/internal"
	"github.com/snonux/gonf/internal/logger"
	"github.com/snonux/gonf/resource"
)

// CLI parses flags and runs or lists tasks. Returns a process exit code.
//
//	gonf -version
//	gonf -list
//	gonf -profile=fedora
//	gonf -verbose | -quiet
//	gonf -dry-run | -n
//	gonf <task> [task...]
func CLI() int {
	fs := flag.NewFlagSet(os.Args[0], flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	version := fs.Bool("version", false, "Print version")
	list := fs.Bool("list", false, "List registered tasks")
	profile := fs.String("profile", "", "Override detected profile (fedora, rocky, ...)")
	verbose := fs.Bool("verbose", false, "Debug logging")
	quiet := fs.Bool("quiet", false, "Only warnings and errors (summary still printed)")
	dryRun := fs.Bool("dry-run", false, "Preview changes without applying them")
	dryRunShort := fs.Bool("n", false, "Alias for -dry-run")

	if err := fs.Parse(os.Args[1:]); err != nil {
		return 2
	}

	switch {
	case *verbose:
		logger.SetLevel(logger.LevelDebug)
	case *quiet:
		logger.SetLevel(logger.LevelWarn)
	default:
		logger.SetLevel(logger.LevelInfo)
	}

	if *dryRun || *dryRunShort {
		resource.SetDryRun(true)
	}

	if *profile != "" {
		SetProfileOverride(*profile)
	}

	// Resolve When guards after -profile so init()-queued tasks see CLI facts.
	Activate(DetectFacts())

	if *version {
		fmt.Println(internal.Version)
		return 0
	}

	if *list {
		infos := Tasks()
		if len(infos) == 0 {
			fmt.Fprintln(os.Stderr, "no tasks registered")
			return 1
		}
		for _, t := range infos {
			if t.Description != "" {
				fmt.Printf("%s\t%s\n", t.Name, t.Description)
			} else {
				fmt.Println(t.Name)
			}
		}
		return 0
	}

	names := fs.Args()
	if len(names) == 0 {
		fmt.Fprintln(os.Stderr, "usage: gonf [-list] [-version] [-profile=...] [-verbose|-quiet] [-dry-run|-n] <task> [task...]")
		return 2
	}

	if err := Run(names...); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	return 0
}
