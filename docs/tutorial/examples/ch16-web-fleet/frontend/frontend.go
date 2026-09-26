// Package frontend holds the web front end tasks of the tutorial's
// chapter 16 recipe.
package frontend

//go:generate go run github.com/snonux/gonf/cmd/gonf-desc

import (
	//lint:ignore ST1001 recipes use the unqualified gonf DSL.
	. "github.com/snonux/gonf/api"
)

// Frontend's methods become frontend_* tasks. main registers them on the
// frontends cluster, so each one applies only on its hosts.
type Frontend struct{}

// Lodge is per-host data: when this front end rotates its logs.
type Lodge struct{ RotateAt string }

const docroot = "/var/www/gonfy"

// Packages installs Apache httpd and its tools.
func (Frontend) Packages() { Packages("httpd", "httpd-tools") }

// Account creates the deploy account that owns the site's files.
func (Frontend) Account() {
	User("gonfy-deploy", WithSystem, WithPrimaryGroup("gonfy-deploy"),
		WithHome(docroot), WithShell("/sbin/nologin"))
}

// Content publishes the site to the document root. The tree is synced
// from the controller; its start page renders on each front end.
func (Frontend) Content() {
	owner := Perm(0o755, "gonfy-deploy:gonfy-deploy")
	Dir(docroot, owner)
	// index.html.tmpl renders on the destination, so every front end
	// names itself. WithPrune removes pages that left the source tree.
	Dir(docroot+"/htdocs", WithSource("assets/web/htdocs"), WithPrune,
		owner, WithFileMode(0o644))
}

// OptsContent adds task options: the owner must exist first.
func (Frontend) OptsContent() TaskOptions { return TaskOptions{Needs(Frontend.Account)} }

// Config installs and validates the httpd configuration and keeps httpd
// running.
func (Frontend) Config() {
	// The status page's password file, straight from the secret provider.
	SecretFile("/etc/httpd/gonfy.htpasswd", "web/status-htpasswd", Perm(0o640, "root:apache"))
	// httpd.conf includes gonfy.conf, so they are checked together: httpd
	// parses the staged pair, and only a valid pair goes live.
	set := ConfigSet("httpd",
		ConfigFile("httpd.conf", "/etc/httpd/conf/httpd.conf", WithContent(httpdConf), RootOwned),
		ConfigFile("gonfy.conf", "/etc/httpd/conf/gonfy.conf", WithSource("assets/web/gonfy.conf"), RootOwned),
		WithSetValidation("httpd", List("-t", "-f", MemberPath("httpd.conf"))))
	// Started and enabled on every apply; reloaded only when the set changed.
	Service("httpd", WithReload, OnChange(set))
}

// OptsConfig adds task options: httpd must be installed and the document
// root published before the config that points at it.
func (Frontend) OptsConfig() TaskOptions {
	return TaskOptions{Needs(Frontend.Packages, Frontend.Content)}
}

// Tuning raises the listen backlog for busy front ends.
func (Frontend) Tuning() {
	conf := File("/etc/sysctl.d/90-gonfy-web.conf",
		WithContent("net.core.somaxconn = 1024\n"), RootOwned)
	Command("sysctl", List("-p", "/etc/sysctl.d/90-gonfy-web.conf"),
		WithName("sysctl-web"), OnChange(conf))
}

// Firewall opens HTTP in firewalld, on front ends that have firewalld.
func (Frontend) Firewall() {
	// The destination checks the path, so a front end without firewalld
	// skips the block.
	WhenPathExists("/usr/bin/firewall-cmd", func() {
		open := Command("firewall-cmd", List("--permanent", "--add-service=http"),
			WithName("firewall-http"),
			Unless("firewall-cmd", List("--permanent", "--query-service=http")))
		Command("firewall-cmd", List("--reload"), WithName("firewall-reload"),
			OnChange(open))
	})
}

// Logrotate rotates the httpd logs nightly, each front end at its own time.
func (Frontend) Logrotate() {
	File("/usr/local/sbin/gonfy-rotate", WithSource("assets/web/gonfy-rotate"), RootExec)
	// One timer per front end, at the RotateAt from its inventory entry,
	// so the front ends never reload at the same moment.
	EachHost(func(l Lodge) {
		SystemdTimer("gonfy-rotate",
			WithCommand("/usr/local/sbin/gonfy-rotate"),
			WithOnCalendar("*-*-* "+l.RotateAt), WithPersistent,
			WithDescription("Rotate Gonfy's httpd logs"))
	})
}
