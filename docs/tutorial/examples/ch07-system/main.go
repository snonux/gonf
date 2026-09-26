// Command recipe manages packages, services, timers, cron jobs and users
// (tutorial chapter 7).
package main

import (
	//lint:ignore ST1001 recipes use the unqualified gonf DSL.
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
