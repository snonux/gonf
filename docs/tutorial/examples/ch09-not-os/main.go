// Command gonf leaves Gonfy's macOS Git settings alone (chapter 9).
package main

import (
	//lint:ignore ST1001 recipes use the unqualified gonf DSL.
	. "github.com/snonux/gonf/api"
	"github.com/snonux/gonf/cli"
)

func main() {
	note := func(name string) func() {
		return func() {
			File(DestHome("gonfy-"+name), WithContent("Gonfy's portable settings\n"), WithMode(0o644))
		}
	}
	// The positive spelling remains valid.
	Task("unix", "All managed OSes except macOS", note("unix"),
		WhenOS("linux", "freebsd", "openbsd", "netbsd"))
	// The exclusion spelling records the destination's decision too.
	Task("not-mac", "Gonfy's portable settings except macOS", note("not-mac"),
		WhenNotOS("darwin"))
	cli.Main()
}
