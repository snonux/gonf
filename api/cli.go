package api

import (
	"flag"
	"fmt"
	"os"

	"github.com/snonux/gonf/internal"
)

// CLI parses flags and runs or lists tasks. Returns a process exit code.
//
//	gonf -version
//	gonf -list
//	gonf <task> [task...]
func CLI() int {
	fs := flag.NewFlagSet(os.Args[0], flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	version := fs.Bool("version", false, "Print version")
	list := fs.Bool("list", false, "List registered tasks")

	if err := fs.Parse(os.Args[1:]); err != nil {
		return 2
	}

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
		fmt.Fprintln(os.Stderr, "usage: gonf [-list] [-version] <task> [task...]")
		return 2
	}

	if err := Run(names...); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	return 0
}
