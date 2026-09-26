// Command recipe edits and validates files (tutorial chapter 4).
package main

import (
	"os"

	//lint:ignore ST1001 recipes use the unqualified gonf DSL.
	. "github.com/snonux/gonf/api"
	"github.com/snonux/gonf/cli"
)

func main() {
	Task("lines", "Own single lines and a block of ~/gonf-tutorial/app.conf", lines)
	Task("script", "Install a shell script only if it parses", script)
	Task("greeter", "Install two scripts that must work together", greeter)
	cli.Main()
}

func lines() {
	File(DestHome("gonf-tutorial/app.conf"),
		// Exact lines: added when missing, removed when present.
		WithLines("log_level=info", "color=yes"),
		WithoutLines("debug=true"),
		// Own "the line starting with port=", whatever its value is today.
		WithKeyedLine("port=", "port=8080"),
		// Own everything between # BEGIN GONF peers and # END GONF peers.
		WithBlock("peers", "peer=10.0.0.1", "peer=10.0.0.2"),
		// Own an rc.conf-style shell variable: greeting="hello world".
		WithShellVar("greeting", "hello world"),
		WithMode(0o644))
}

func script() {
	body := envOr("GREETING_SCRIPT", "#!/bin/sh\necho hello\n")
	// WithValidation needs an absolute path, and DestHome records the
	// placeholder ${HOME}. Home is the controller's home: the same machine
	// for a local run like this one.
	File(Home("gonf-tutorial/hello.sh"),
		WithContent(body), WithMode(0o755),
		// gonf writes a candidate file, runs "sh -n <candidate>" and only
		// replaces the live file when that exits 0.
		WithValidation("sh", List("-n", CandidatePath)))
}

func greeter() {
	lib := "greet() { echo \"hello, $1\"; }\n"
	main := "#!/bin/sh\n. " + MemberPath("lib.sh") + "\ngreet world\n"
	Dir(DestHome("gonf-tutorial/greeter"), WithMode(0o755))
	set := ConfigSet("greeter",
		ConfigFile("lib.sh", DestHome("gonf-tutorial/greeter/lib.sh"), WithContent(lib), WithMode(0o644)),
		ConfigFile("main.sh", DestHome("gonf-tutorial/greeter/main.sh"), WithContent(main), WithMode(0o755)),
		// Both files are staged together, then the validator runs the
		// staged main.sh, which sources the staged lib.sh.
		WithSetValidation("sh", List(MemberPath("main.sh"))))
	// ${HOME} is not expanded in argv, but it is in WithDir.
	Command("sh", List("main.sh"), WithDir(DestHome("gonf-tutorial/greeter")),
		WithName("run-greeter"), OnChange(set))
}

// envOr returns the environment variable key, or def when it is unset.
// The tutorial uses it to feed a broken script into the "script" task.
func envOr(key, def string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return def
}
