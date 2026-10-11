// Command gonf shares task registration options (tutorial chapter 8).
package main

import (
	//lint:ignore ST1001 recipes use the unqualified gonf DSL.
	. "github.com/snonux/gonf/api"
	"github.com/snonux/gonf/cli"
)

// Roof holds Gonfy’s roof task.
type Roof struct{}

// Door holds Gonfy’s door task.
type Door struct{}

// Seal installs the roof setting.
func (Roof) Seal() {
	File(DestHome("gonf-tutorial/roof.conf"), WithContent("sealed=yes\n"), WithMode(0o644))
}

// Hang installs the door setting.
func (Door) Hang() {
	File(DestHome("gonf-tutorial/door.conf"), WithContent("hung=yes\n"), WithMode(0o644))
}

// OptsHang makes the roof task a prerequisite of hanging the door.
func (Door) OptsHang() TaskOptions { return TaskOptions{Needs(Roof.Seal)} }

func main() {
	// Equivalent separate calls:
	// RegisterMethods(Roof{}, WithPrefix("lodge_"), WhenLinux())
	// RegisterMethods(Door{}, WithPrefix("lodge_"), WhenLinux())
	RegisterGroup([]any{Roof{}, Door{}}, WithPrefix("lodge_"), WhenLinux())
	AggregatePrefix("lodge")
	cli.Main()
}
