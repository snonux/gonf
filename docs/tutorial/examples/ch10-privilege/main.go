// Command recipe mixes unprivileged and root work (tutorial chapter 10).
package main

import (
	//lint:ignore ST1001 recipes use the unqualified gonf DSL.
	. "github.com/snonux/gonf/api"
	"github.com/snonux/gonf/cli"
)

// System's tasks all apply as root: embedding RequiresRoot is the same as
// adding Privileged() to every method.
type System struct{ RequiresRoot }

// DescMotd returns the -list description of the Motd task.
func (System) DescMotd() string { return "Write /etc/motd.d/gonf-tutorial" }

// Motd writes a message of the day fragment.
func (System) Motd() {
	EnsureDir("/etc/motd.d", RootOwned)
	File("/etc/motd.d/gonf-tutorial", WithContent("Managed by gonf. Gonfy keeps this lodge tidy.\n"), RootOwned)
}

// DescHosts returns the -list description of the Hosts task.
func (System) DescHosts() string { return "Own a block of /etc/hosts" }

// Hosts owns a block of /etc/hosts and leaves every other line alone.
func (System) Hosts() {
	File("/etc/hosts", WithBlock("tutorial", "10.0.0.1 earth", "10.0.0.2 mars"), RootOwned)
}

// DescNote returns the -list description of the Note task.
func (System) DescNote() string { return "A user file, opted out of RequiresRoot" }

// Note writes into the user's home, so it opts out of root.
func (System) Note() {
	File(DestHome(".tutorial-note"), WithContent("hi from gonfy\n"), WithMode(0o644))
}

// OptsNote opts the Note task out of the struct's RequiresRoot.
func (System) OptsNote() TaskOptions { return TaskOptions{Unprivileged()} }

func main() {
	RegisterMethods(System{}) // system_motd, system_hosts, system_note
	Task("dotfile", "An unprivileged task with one root command", func() {
		File(DestHome(".tutorial-inputrc"), WithContent("set editing-mode vi\n"), WithMode(0o644))
		Command("id", List("-un"), WithName("whoami-as-root"), WithElevate)
	})
	cli.Main()
}
