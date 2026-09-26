// Command gonf describes an inventory and pushes to it (tutorial
// chapter 12).
package main

import (
	//lint:ignore ST1001 recipes use the unqualified gonf DSL.
	. "github.com/snonux/gonf/api"
	"github.com/snonux/gonf/cli"
)

// Window is per-host data every planet carries: when it may run its
// maintenance job.
type Window struct{ Hour string }

// Mirror is per-host data only some planets carry: where they mirror to.
type Mirror struct{ Target string }

// Planet's tasks apply to the "inner" cluster.
type Planet struct{}

// DescMotd returns the -list description of the Motd task.
func (Planet) DescMotd() string { return "Greet with the host's name" }

// Motd writes a message of the day fragment. Destination templates see
// the destination's own facts, so every host renders its own name.
func (Planet) Motd() {
	EnsureDir("/etc/motd.d", RootOwned)
	File("/etc/motd.d/welcome", WithContent("Welcome to {{ .Gonf.Hostname }}! Gonfy waves hello.\n"), WithTemplate,
		RootOwned)
}

// DescMaintenance returns the -list description of the Maintenance task.
func (Planet) DescMaintenance() string { return "Per-host maintenance window" }

// Maintenance installs a cron job at each host's own hour.
func (Planet) Maintenance() {
	EachHost(func(w Window) {
		CronAt("maintenance", "0 "+w.Hour+" * * *", "/usr/local/bin/maintenance")
	})
}

// DescMirror returns the -list description of the Mirror task.
func (Planet) DescMirror() string { return "Mirror job, on hosts with a Mirror" }

// Mirror installs a mirror job on the hosts that have Mirror data;
// EachHostWith skips the others instead of failing.
func (Planet) Mirror() {
	EachHostWith(func(m Mirror) {
		CronAt("mirror", "30 * * * *", "rsync -a /srv/ "+m.Target+":/srv/")
	})
}

// DescEarthOnly returns the -list description of the EarthOnly task.
func (Planet) DescEarthOnly() string { return "Only on earth, within the cluster" }

// EarthOnly shows a task narrowed to part of its cluster.
func (Planet) EarthOnly() {
	File("/etc/motd.d/earth", WithContent("Mostly harmless.\n"), RootOwned)
}

// WhenEarthOnly narrows EarthOnly to earth, on top of the cluster guard.
func (Planet) WhenEarthOnly() TaskOption { return WhenHostnameIn("earth") }

func main() {
	// A bundle of defaults shared by the hosts on the LAN.
	lan := HostDefaults(WithSSHUser("paul"), WithSSHDomain("lan"),
		WithPrivilege(PrivilegeSudo), WithData(Window{Hour: "3"}))
	earth := Host("earth", lan, WithData(Mirror{Target: "mars.lan"}))
	mars := Host("mars", lan, WithData(Window{Hour: "4"}))
	pluto := Host("pluto", WithSSHUser("paul"), WithSSHHost("pluto.example.org"),
		WithPrivilege(PrivilegeSudo), WithData(Window{Hour: "5"}))

	inner := Cluster("inner", earth, mars).Parallel(2)
	outer := Cluster("outer", pluto)
	Fleet("solar", inner, outer)

	// Every Planet method binds to the inner cluster (planet_* tasks), and
	// only applies on hosts whose name contains one of its members.
	RegisterOnCluster("inner", Planet{}, Privileged())
	cli.Main()
}
