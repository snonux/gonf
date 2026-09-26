// Command gonf runs commands and wires change gates (tutorial chapter 6).
package main

import (
	//lint:ignore ST1001 recipes use the unqualified gonf DSL.
	. "github.com/snonux/gonf/api"
	"github.com/snonux/gonf/cli"
)

func main() {
	Task("commands", "Idempotent commands and change gates", commands)
	cli.Main()
}

func commands() {
	dir := DestHome("gonf-tutorial/commands")
	Dir(dir, WithMode(0o755))

	// Runs once: skipped as soon as the file it creates exists.
	Command("git", List("init", "-q", "repo"), WithDir(dir), Creates(dir+"/repo/.git"),
		WithName("git-init"))

	// Guards run on the destination: Unless skips when the check succeeds,
	// OnlyIf runs only when it succeeds. Like any argv, guard arguments are
	// not ${HOME}-expanded, so this uses Home (the controller's home, the
	// same machine for a local run).
	repo := Home("gonf-tutorial/commands/repo")
	Command("git", List("-C", repo, "config", "user.name", "gonfy"), WithName("git-user"),
		OnlyIf("test", List("-d", repo)),
		Unless("git", List("-C", repo, "config", "user.name"), ExpectStdout("gonfy")))

	// Sh splits a command line like a shell would, but runs no shell.
	Sh("echo 'hello from Gonfy'", WithName("echo"))

	// A change gate: the command runs only when the file changed.
	conf := File(dir+"/app.conf", WithContent("workers=4\n"), WithMode(0o644))
	Command("sh", List("-c", "echo reloading app; wc -l app.conf"), WithDir(dir),
		WithName("reload-app"), OnChange(conf))

	// DependsOn orders resources without gating them.
	done := Noop("commands-done")
	Command("sh", List("-c", "echo Gonfy says all set"), WithName("report"), DependsOn(done))
}
