# 6. Commands and change gates

When no resource fits, run a command. A command is a resource like any
other, so it should be idempotent too: gonf gives you guards that decide on
the destination whether to run it, and change gates that run it only when
something it depends on changed.

> 🦫 **Gonfy says:** A command is a stick without a shape of its own. Give it a guard, so it knows when the job is already done.

## The recipe

```go
// Command gonf runs commands and wires change gates (tutorial chapter 6).
package main

import (
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
```

## First run

```text
$ ./gonf commands
2026/09/26 08:26:52 created directory /home/paul/gonf-tutorial/commands
2026/09/26 08:26:52 running Command[git-init]: git init -q repo
2026/09/26 08:26:52 running Command[git-user]: git -C /home/paul/gonf-tutorial/commands/repo config user.name gonfy
2026/09/26 08:26:52 running Command[echo]: echo hello from Gonfy
2026/09/26 08:26:52 updated /home/paul/gonf-tutorial/commands/app.conf
2026/09/26 08:26:52 running Command[reload-app]: sh -c echo reloading app; wc -l app.conf
2026/09/26 08:26:52 running Command[report]: sh -c echo Gonfy says all set
summary: 1 ok, 7 changed, 0 skipped, 0 would-change
  changed Directory[/home/paul/gonf-tutorial/commands]
  changed Command[git-init]
  changed Command[git-user]
  changed Command[echo]
  changed File[/home/paul/gonf-tutorial/commands/app.conf]
  changed Command[reload-app]
  changed Command[report]
```

## Second run

> 🦫 **Gonfy says:** Skipped does not mean failed. It means the job was already done.

```text
$ ./gonf commands
2026/09/26 08:26:52 skipping Command[git-init]: /home/paul/gonf-tutorial/commands/repo/.git already exists
2026/09/26 08:26:52 skipping Command[git-user]: unless guard succeeded
2026/09/26 08:26:52 running Command[echo]: echo hello from Gonfy
2026/09/26 08:26:52 skipping Command[reload-app]: no watched dependency changed
2026/09/26 08:26:52 running Command[report]: sh -c echo Gonfy says all set
summary: 3 ok, 2 changed, 3 skipped, 0 would-change
  changed Command[echo]
  changed Command[report]
```

Each command was held back for its own reason:

| Command | Option | Why it was skipped |
|---------|--------|--------------------|
| `git-init` | `Creates(path)` | the path it creates already exists |
| `git-user` | `Unless(bin, args, ExpectStdout("gonfy"))` | the check printed `gonfy`, so the work is done |
| `reload-app` | `OnChange(conf)` | `app.conf` did not change in this run |

`echo` and `report` have no guard, so they run every time. That is fine for
a harmless `echo`; for real work, add a guard.

## Change the config

> 🦫 **Gonfy says:** A new stick in `app.conf`, so the reload runs. No change, no reload.

```text
$ echo workers=8 > ~/gonf-tutorial/commands/app.conf
$ ./gonf commands
2026/09/26 08:26:52 skipping Command[git-init]: /home/paul/gonf-tutorial/commands/repo/.git already exists
2026/09/26 08:26:52 skipping Command[git-user]: unless guard succeeded
2026/09/26 08:26:52 running Command[echo]: echo hello from Gonfy
2026/09/26 08:26:52 updated /home/paul/gonf-tutorial/commands/app.conf
2026/09/26 08:26:52 running Command[reload-app]: sh -c echo reloading app; wc -l app.conf
2026/09/26 08:26:52 running Command[report]: sh -c echo Gonfy says all set
summary: 2 ok, 4 changed, 2 skipped, 0 would-change
  changed Command[echo]
  changed File[/home/paul/gonf-tutorial/commands/app.conf]
  changed Command[reload-app]
  changed Command[report]
```

`app.conf` changed, so `reload-app` ran. This is the pattern for "restart
the daemon when its config changed":

![A change gate: reload-app runs only when app.conf changed](img/ch06-1.svg)

`OnChange` also orders: `reload-app` always applies after `app.conf`. Use
`DependsOn` when you want only the ordering, as `report` does with the
`Noop` marker. The same `OnChange` works on `Service` (restart or reload),
`Timer` and `DaemonReload` (chapter 7).

## What the plan carries

```text
$ ./gonf plan -redacted commands
{"op":"plan_preview","version":25,"id":"plan"}
{"op":"dir","id":"Directory[${HOME}/gonf-tutorial/commands]","path":"${HOME}/gonf-tutorial/commands","mode":"0755"}
{"op":"command","id":"Command[git-init]","name":"git-init","bin":"git","args":["init","-q","repo"],"dir":"${HOME}/gonf-tutorial/commands","creates":"${HOME}/gonf-tutorial/commands/repo/.git"}
{"op":"command","id":"Command[git-user]","name":"git-user","bin":"git","args":["-C","/home/paul/gonf-tutorial/commands/repo","config","user.name","gonfy"],"unless":{"bin":"git","args":["-C","/home/paul/gonf-tutorial/commands/repo","config","user.name"],"expect_stdout":"gonfy"},"only_if":{"bin":"test","args":["-d","/home/paul/gonf-tutorial/commands/repo"]}}
{"op":"command","id":"Command[echo]","name":"echo","bin":"echo","args":["hello from Gonfy"]}
{"op":"file","id":"File[${HOME}/gonf-tutorial/commands/app.conf]","path":"${HOME}/gonf-tutorial/commands/app.conf","mode":"0644","content_b64":"d29ya2Vycz00Cg==","has_content":true}
{"op":"command","id":"Command[reload-app]","name":"reload-app","bin":"sh","args":["-c","echo reloading app; wc -l app.conf"],"dir":"${HOME}/gonf-tutorial/commands","if_changed":true,"watch":["File[${HOME}/gonf-tutorial/commands/app.conf]"],"deps":["File[${HOME}/gonf-tutorial/commands/app.conf]"]}
{"op":"noop","id":"Noop[commands-done]","name":"commands-done"}
{"op":"command","id":"Command[report]","name":"report","bin":"sh","args":["-c","echo Gonfy says all set"],"deps":["Noop[commands-done]"]}
wrote redacted preview to stdout (9 ops, 0 secret-bearing; not a plan, cannot be applied)
```

Each guard travels with its command (`creates`, `unless`, `only_if`), so
the destination checks it just before running. `OnChange(conf)` becomes
`if_changed` plus `watch`, and `DependsOn` becomes `deps`. The git arguments
hold `/home/paul`, not `${HOME}`, because the argv is not expanded (next
section).

## Guards and arguments

- Guards (`Creates`, `Unless`, `OnlyIf`) run on the destination.
- `WithDir`, `Creates` and other path options expand `${HOME}`; the argv
  does not. That is why the recipe uses `Home(...)` in the git arguments.
- `Sh("echo 'hello from Gonfy'")` splits the line like a shell would, but
  runs no shell: pipes, `$VAR` and globs are refused. For real shell syntax
  write `Command("sh", List("-c", "..."))`.
- Without `WithName`, a command's ID is its whole argv. Name it when you
  want to watch it or when the argv holds a secret (chapter 13).

Reference: [Command](../reference.md#command),
[Change gates](../reference.md#change-gates),
[Shared options](../reference.md#shared-options).

---

← [5. Templates](05-templates.md) · [Contents](README.md) · Next: [7. Packages, services, timers, cron and users](07-system-resources.md) →
