// Command gonf manages packages, services, timers, cron jobs and users
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
	Task("cron", "A cron job in root's crontab", cronJob)
	Task("account", "A service account for Gonfy", account, Privileged())
	Task("extras", "More system options, for a plan preview", extras, Privileged(), WhenLinux())
	Task("daemon", "An OpenBSD daemon account in its own login class", daemon, Privileged(), WhenOpenBSD())
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
		// Also run 10 minutes after each boot.
		WithOnBootSec("10min"),
		WithDescription("Nightly backup"),
		WithServiceDescription("Run the nightly backup once"),
		// The .service starts after the network is up, and pulls it in.
		WithAfter("network-online.target"), WithWants("network-online.target"))
}

func cronJob() {
	CronAt("gonfy-uptime", "*/15 * * * *", "uptime >> /tmp/uptime.log",
		WithCronUser("root"))
}

func account() {
	User("gonfy", WithPrimaryGroup("gonfy"), WithShell("/bin/sh"),
		WithHome("/home/gonfy"), WithCreateHome)
}

func extras() {
	// Environment variables for the package tool, while probing and installing.
	Package("gonfy-tools", WithEnv(map[string]string{"http_proxy": "http://proxy.lodge:3128"}))

	// Your own unit file, one daemon-reload after it changes, then the
	// service that uses it, restarted only when the unit changed.
	unit := File("/etc/systemd/system/gonfy-web.service", WithContent(webUnit), RootOwned)
	SystemdUnits(FanIn(unit), ActivateService("gonfy-web", WithRestart))

	// Enabled for the next boot, but not started now.
	Timer("fstrim", WithEnableOnly)

	// Cron's long form, one field at a time. WithLegacyCommand first
	// removes a hand-written entry that ran the old script, on any schedule.
	Cron("gonfy-report", WithCommand("/usr/local/bin/report.sh"), WithCronUser("gonfy"),
		WithMinute("30"), WithHour("6"), WithWeekday("1-5"),
		WithLegacyCommand("/usr/local/bin/old-report.sh"))

	// Move Gonfy's home: WithManageHome rewrites the home field of the
	// existing account. It moves no files, so declare the new directory.
	account := User("gonfy", WithHome("/srv/gonfy"), WithManageHome)
	Dir("/srv/gonfy", WithOwner("gonfy:gonfy"), WithMode(0o750), DependsOn(account))
}

const webUnit = `[Unit]
Description=Gonfy's web server

[Service]
ExecStart=/usr/local/bin/gonfy-web

[Install]
WantedBy=multi-user.target
`

func daemon() {
	// /etc/login.conf.d/gonfyd: resource limits for the daemon's class.
	class := LoginClass("gonfyd", "", WithContent("gonfyd:\\\n\t:openfiles=4096:\\\n\t:tc=daemon:\n"))
	// The account is created in that class (BSD only).
	User("_gonfyd", WithLoginClass("gonfyd"), WithShell("/sbin/nologin"), DependsOn(class))
}
