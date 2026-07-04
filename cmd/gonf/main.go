package main

import (
	"flag"
	"fmt"
	"os"

	"codeberg.org/snonux/gonf/examples"
	"codeberg.org/snonux/gonf/internal"
	"codeberg.org/snonux/gonf/internal/resources"
)

func main() {
	version := flag.Bool("version", false, "Print version")

	if *version {
		fmt.Println(internal.Version)
		os.Exit(0)
	}

	resources.Init()

	if err := examples.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "example run error: %v\n", err)
		os.Exit(1)
	}
}
