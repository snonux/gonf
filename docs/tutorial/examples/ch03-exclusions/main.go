// Command gonf syncs a tree with exclusions (tutorial chapter 3).
package main

import (
	//lint:ignore ST1001 recipes use the unqualified gonf DSL.
	. "github.com/snonux/gonf/api"
	"github.com/snonux/gonf/cli"
)

func postcards() {
	// The ordinary tree form remains valid:
	// Dir(DestHome("gonf-tutorial/postcards"), WithSource(Home("gonf-tutorial/source")), WithPrune)
	// Gonfy keeps scratch notes and private postcards out of this sync.
	Dir(DestHome("gonf-tutorial/postcards"),
		WithSource(Home("gonf-tutorial/source")), WithExclude("private", "*.tmp"),
		WithPrune, WithMode(0o755), WithFileMode(0o644))
}

func main() {
	Task("postcards", "Sync Gonfy's public postcards", postcards)
	cli.Main()
}
