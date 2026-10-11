// Command gonf checks controller sources (tutorial chapter 3).
package main

import (
	//lint:ignore ST1001 recipes use the unqualified gonf DSL.
	. "github.com/snonux/gonf/api"
	"github.com/snonux/gonf/cli"
)

func optional() {
	source := Home("gonf-tutorial/source")
	if !OptionalControllerDir(source, "Gonfy's optional postcards") {
		return
	}
	Dir(DestHome("gonf-tutorial/postcards"), WithSource(source), WithMode(0o755))
}

func required() {
	source := RequireControllerFile(Home("gonf-tutorial/source/banner.txt"),
		"Gonfy's lodge banner", "Restore banner.txt in the controller's source checkout")
	if source == "" {
		return
	}
	File(DestHome("gonf-tutorial/banner.txt"), WithSource(source), WithMode(0o644))
}

func main() {
	Task("optional", "Copy postcards only when the controller checkout exists", optional)
	Task("required", "Install Gonfy's required banner", required)
	cli.Main()
}
