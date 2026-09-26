# 7. Packages, services, timers, cron and users

> 🦫 **Gonfy says:** This is the chapter where I get a real account on the machine, `gonfy`, with a home of my own. Chapter 10 logs in as me.

These resources talk to the operating system's own tools. gonf picks the
backend on the destination:

| Resource | Linux | OpenBSD | FreeBSD | NetBSD |
|----------|-------|---------|---------|--------|
| `Package` | `dnf` (Fedora, RHEL, Rocky) | `pkg_add` | `pkg` | `pkgin` |
| `Service` | `systemctl` | `rcctl` | `service` | `service` |
| `Timer`, `SystemdTimer`, `DaemonReload` | systemd | | | |
| `Cron` | `crontab` | `crontab` | `crontab` | `crontab` |
| `User` | `useradd`, `usermod` | `useradd`, `usermod` | `pw` | `useradd`, `usermod` |

## The recipe

```go
// Command recipe manages packages, services, timers, cron jobs and users
// (tutorial chapter 7).
package main

import (
	. "github.com/snonux/gonf/api"
	"github.com/snonux/gonf/cli"
)

func main() {
	Task("webserver", "Install, configure and run nginx", webserver, Privileged())
	Task("backup", "Nightly backup as a systemd timer", backup, Privileged(), WhenLinux())
	Task("cron", "A cron job in the current user's crontab", cronJob)
	Task("account", "A service account for Gonfy", account, Privileged())
	cli.Main()
}

func webserver() {
	pkg := Package("nginx")
	conf := File("/etc/nginx/conf.d/gonfy.conf",
		WithContent("server { listen 8080; }\n"), RootOwned, DependsOn(pkg))
	// Started and enabled on every apply; restarted only when conf changed.
	Service("nginx", WithRestart, OnChange(conf))
}

func backup() {
	SystemdTimer("backup",
		WithCommand("/usr/local/bin/backup.sh"),
		WithOnCalendar("*-*-* 03:00:00"), WithPersistent,
		WithDescription("Nightly backup"))
}

func cronJob() {
	CronAt("gonfy-uptime", "*/15 * * * *", "uptime >> /tmp/uptime.log",
		WithCronUser("root"))
}

func account() {
	User("gonfy", WithPrimaryGroup("gonfy"), WithShell("/bin/sh"),
		WithHome("/home/gonfy"), WithCreateHome)
}
```

`RootOwned` is `Perm(0o644, Root)`: owned by root and the destination's root
group (chapter 10).

## Preview what would happen

The machine used for this book is an Ubuntu container with neither `dnf`
nor a running systemd, so `webserver` and `backup` cannot apply here. A plan
preview shows what they would do on a Fedora host:

```text
$ ./gonf plan -redacted webserver backup
{"op":"plan_preview","version":21,"id":"plan"}
{"op":"package","id":"Package[nginx]","name":"nginx","elevate":true}
{"op":"file","id":"File[/etc/nginx/conf.d/gonfy.conf]","path":"/etc/nginx/conf.d/gonfy.conf","mode":"0644","owner":"root","group":"0","content_b64":"c2VydmVyIHsgbGlzdGVuIDgwODA7IH0K","has_content":true,"elevate":true,"deps":["Package[nginx]"]}
{"op":"service","id":"Service[nginx]","name":"nginx","restart":true,"if_changed":true,"watch":["File[/etc/nginx/conf.d/gonfy.conf]"],"elevate":true,"deps":["File[/etc/nginx/conf.d/gonfy.conf]"]}
{"op":"when_begin","id":"when.backup","elevate":true,"all":[{"fact":"goos","eq":"linux"}]}
{"op":"systemd_timer","id":"SystemdTimer[backup]","name":"backup","command":"/usr/local/bin/backup.sh","on_calendar":"*-*-* 03:00:00","persistent":true,"description":"Nightly backup","elevate":true}
{"op":"when_end","elevate":true}
wrote redacted preview to stdout (7 ops, 0 secret-bearing; not a plan, cannot be applied)
```

Read it as: install `nginx`; then write the config (`deps` on the package);
then keep `nginx` started and enabled, restarting it only when the config
changed (`if_changed` with `watch`). The timer sits inside a
`when_begin`/`when_end` block, so a non-Linux destination skips it.

![Package, then config file, then a service restarted on change](img/ch07-1.svg)

## Cron

`CronAt(name, schedule, command)` owns one job in a crontab, between marker
comments, and leaves the rest of the crontab alone:

```text
$ crontab -l
no crontab for root
[exit status 1]
$ ./gonf cron
2026/09/26 08:23:14 updated crontab for root (job gonfy-uptime)
summary: 0 ok, 1 changed, 0 skipped, 0 would-change
  changed Cron[root/gonfy-uptime]
$ crontab -l
# BEGIN GONF Cron[gonfy-uptime]
*/15 * * * * uptime >> /tmp/uptime.log
# END GONF Cron[gonfy-uptime]
$ ./gonf cron
summary: 1 ok, 0 changed, 0 skipped, 0 would-change
```

## Users

`User` only adds: it never deletes accounts, removes group memberships or
sets passwords.

```text
$ ./gonf -n account
2026/09/26 08:23:14 dry-run: would run groupadd -- gonfy
2026/09/26 08:23:14 dry-run: would run useradd --create-home --gid gonfy --home /home/gonfy --shell /bin/sh -- gonfy
summary: 0 ok, 0 changed, 0 skipped, 2 would-change
  would-change Group[gonfy]
  would-change User[gonfy]
$ ./gonf account
summary: 0 ok, 2 changed, 0 skipped, 0 would-change
  changed Group[gonfy]
  changed User[gonfy]
```

```text
$ ./gonf account
summary: 1 ok, 0 changed, 0 skipped, 0 would-change
$ id gonfy
uid=1001(gonfy) gid=1002(gonfy) groups=1002(gonfy)
```

## More system resources

| Call | What it does |
|------|--------------|
| `Packages("git", "tmux")`, `Package(name, IsLatest)` | several packages, or keep one upgraded |
| `Service(name, WithReload)`, `WithFlags(...)` | reload instead of restart; BSD rc flags |
| `Timer(name)` | enable and start an existing `.timer` unit |
| `SystemdTimer(name, ...)` | write a `.timer` and a oneshot `.service`, reload, enable |
| `SystemdUnits(FanIn(units), ActivateTimer(...))` | your own unit files plus one shared daemon-reload |
| `Cron(name, WithSchedule(...), WithCronEnv(...))` | the long form of `CronAt` |
| `LoginClass(class, src)` | an OpenBSD login class fragment |
| `No*` (`NoPackage`, `NoService`, `NoCron`, ...) | make sure it is gone |

Reference: [Package](../reference.md#package),
[Service and DaemonReload](../reference.md#service-and-daemonreload),
[SystemdUnits](../reference.md#systemdunits), [Timer](../reference.md#timer),
[SystemdTimer](../reference.md#systemdtimer), [Cron](../reference.md#cron),
[User](../reference.md#user), [LoginClass](../reference.md#loginclass).

---

← [6. Commands and change gates](06-commands.md) · [Contents](README.md) · Next: [8. Organizing tasks](08-organizing-tasks.md) →
