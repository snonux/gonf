package main

import (
	"flag"
	"fmt"
	"os"

	"codeberg.org/snonux/gonf/internal"
	"codeberg.org/snonux/gonf/internal/file"
	"codeberg.org/snonux/gonf/internal/resources"
)

func main() {
	version := flag.Bool("version", false, "Print version")

	if *version {
		fmt.Println(internal.Version)
		os.Exit(0)
	}

	resources.Init()
	file.HaveString("/tmp/foo.txt", "hi")
}
