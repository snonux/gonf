# 16. A web fleet

The last chapter puts the book together. Gonfy's website runs on three
Linux front ends with Apache httpd, and one recipe sets up everything on
them: the package, a deploy account, the site's files, a validated httpd
configuration with a password-protected status page, kernel tuning, the
firewall and a nightly log rotation that each front end runs at its own
time.

> 🦫 **Gonfy says:** Three lodges, one blueprint. Build the first by hand and you will build the other two slightly differently; let gonf build all three.

> **About the outputs in this chapter.** They were captured on three
> Red Hat Enterprise Linux 8 containers named `fe1`, `fe2` and `fe3`, with
> systemd running and httpd already installed. A stand-in for `ssh` and `scp` ran each remote command in
> the container of the same name, as root, so `sudo` passed straight
> through. gonf's own output is unchanged; with real hosts you see the same
> lines. Where an output repeats itself for every front end, `[...]` marks
> the part left out.

## The fleet

`main.go` describes the front ends and registers their tasks:

```go
// Command gonf runs Gonfy's website on a fleet of Linux front ends
// (tutorial chapter 16).
package main

import (
	. "github.com/snonux/gonf/api"
	"github.com/snonux/gonf/cli"
	"github.com/snonux/gonf/docs/tutorial/examples/ch16-web-fleet/frontend"
)

func main() {
	// What every front end shares: how to reach it and how to become root.
	fe := HostDefaults(WithSSHUser("paul"), WithSSHDomain("lan"),
		WithPrivilege(PrivilegeSudo))
	fe1 := Host("fe1", fe, WithData(frontend.Lodge{RotateAt: "04:00:00"}))
	fe2 := Host("fe2", fe, WithData(frontend.Lodge{RotateAt: "04:10:00"}))
	fe3 := Host("fe3", fe, WithData(frontend.Lodge{RotateAt: "04:20:00"}))
	Cluster("frontends", fe1, fe2, fe3).Parallel(3)

	// frontend_packages, frontend_account, ... bound to the cluster.
	RegisterOnCluster("frontends", frontend.Frontend{}, Privileged())
	// The task "frontend" runs every frontend_* task.
	AggregatePrefix("frontend")
	cli.Main()
}
```

- `HostDefaults` holds what every front end shares: log in as `paul` on
  `<name>.lan` and become root with sudo (chapter 12).
- Each host carries a `frontend.Lodge` with its own `RotateAt` time.
- `RegisterOnCluster` binds every `Frontend` method to the `frontends`
  cluster as a `frontend_*` task, and `Privileged()` marks them all as
  root work (chapter 10). `AggregatePrefix` adds the task `frontend`,
  which runs them all (chapter 8).

```text
$ ./gonf -list
frontend	Run all frontend_* tasks
frontend_account	Creates the deploy account that owns the site's files [destination-guarded: hostname_contains=fe1|fe2|fe3]
frontend_config	Installs and validates the httpd configuration and keeps httpd running [destination-guarded: hostname_contains=fe1|fe2|fe3]
frontend_content	Publishes the site to the document root [destination-guarded: hostname_contains=fe1|fe2|fe3]
frontend_firewall	Opens HTTP in firewalld, on front ends that have firewalld [destination-guarded: hostname_contains=fe1|fe2|fe3]
frontend_logrotate	Rotates the httpd logs nightly, each front end at its own time [destination-guarded: hostname_contains=fe1|fe2|fe3]
frontend_packages	Installs Apache httpd and its tools [destination-guarded: hostname_contains=fe1|fe2|fe3]
frontend_tuning	Raises the listen backlog for busy front ends [destination-guarded: hostname_contains=fe1|fe2|fe3]
$ ./gonf hosts
fe1	paul@fe1.lan
fe2	paul@fe2.lan
fe3	paul@fe3.lan
$ ./gonf clusters
frontends	j=3	fe1,fe2,fe3
```

## The front end tasks

The tasks live in their own package, one method per task. Their `-list`
descriptions are the doc comments, turned into `desc_gen.go` by
`go generate` (chapter 8):

```go
// Package frontend holds the web front end tasks of the tutorial's
// chapter 16 recipe.
package frontend

//go:generate go run github.com/snonux/gonf/cmd/gonf-desc

import (
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
```

The main httpd configuration is a Go string, because it names its partner
file with `MemberPath`:

```go
package frontend

import (
	. "github.com/snonux/gonf/api"
)

// httpdConf is the main httpd configuration. It includes gonfy.conf by
// MemberPath, so the validator parses the staged copy of it.
var httpdConf = `# Managed by gonf. Gonfy owns the front door of this lodge.
ServerRoot "/etc/httpd"
Listen 80
Include conf.modules.d/*.conf
User apache
Group apache
ServerName localhost
ServerAdmin root@localhost
ErrorLog "logs/error_log"
LogLevel warn
LogFormat "%h %l %u %t \"%r\" %>s %b \"%{Referer}i\" \"%{User-Agent}i\"" combined
CustomLog "logs/access_log" combined
TypesConfig /etc/mime.types
AddDefaultCharset UTF-8
<Directory />
    AllowOverride None
    Require all denied
</Directory>
Include "` + MemberPath("gonfy.conf") + `"
`
```

What each task uses:

| Task | Resources and features | Chapter |
|------|------------------------|---------|
| `frontend_packages` | `Packages` | 7 |
| `frontend_account` | `User`, a system account | 7 |
| `frontend_content` | `Dir` with `Perm`, a tree sync with `WithPrune`, a destination template, `Needs` | 3, 5, 8 |
| `frontend_config` | `SecretFile`, `ConfigSet` with `WithSetValidation`, `Service` with `WithReload` and `OnChange`, `Needs` | 4, 6, 7, 13 |
| `frontend_tuning` | `File`, a `Command` gated by `OnChange` | 6 |
| `frontend_firewall` | `WhenPathExists`, `Command` with `Unless`, a gated reload | 6, 9 |
| `frontend_logrotate` | `File` with `RootExec`, `SystemdTimer` per host with `EachHost` | 7, 12 |

The site's files come from `assets/web` next to the recipe:

```text
$ find assets/web secrets/web -type f
assets/web/gonfy.conf
assets/web/gonfy-rotate
assets/web/htdocs/index.html.tmpl
assets/web/htdocs/gonfy.txt
secrets/web/status-htpasswd
```

`index.html.tmpl` renders on each front end, with its own host name:

```text
<!doctype html>
<html>
<head><title>Gonfy's lodge</title></head>
<body>
<h1>Welcome to Gonfy's lodge</h1>
<p>Served by {{ .Gonf.Hostname }}.</p>
</body>
</html>
```

`secrets/web/status-htpasswd` is the status page's password file, read by
the default secret provider (chapter 13). The tutorial checks in a fake one
(user `gonfy`, password `lodge-keeper`); keep yours out of version control.

## Preview one front end

> 🦫 **Gonfy says:** Look at one lodge before you touch three.

Build your gonf in `docs/tutorial/examples` and preview `fe1`:

```text
$ go build -o gonf ./ch16-web-fleet
$ ./gonf push -privilege=sudo -n paul@fe1.lan frontend
2026/09/26 17:53:55 push paul@fe1.lan: remote plan schema 0 < 27 — syncing gonf binary
2026/09/26 17:53:56 push paul@fe1.lan: remote gonf plan schema now 27
2026/09/26 17:53:56 dry-run: would run groupadd -- gonfy-deploy
2026/09/26 17:53:56 dry-run: would run useradd --no-create-home --system --gid gonfy-deploy --home /var/www/gonfy --shell /sbin/nologin -- gonfy-deploy
2026/09/26 17:53:56 dry-run: would create directory /var/www/gonfy
2026/09/26 17:53:56 dry-run: would create directory /var/www/gonfy/htdocs
2026/09/26 17:53:56 dry-run: would update /var/www/gonfy/htdocs/gonfy.txt
2026/09/26 17:53:56 dry-run: would update /var/www/gonfy/htdocs/index.html
2026/09/26 17:53:56 dry-run: would update /etc/httpd/gonfy.htpasswd
2026/09/26 17:53:56 dry-run: config set httpd would publish /etc/httpd/conf/httpd.conf
2026/09/26 17:53:56 dry-run: config set httpd would publish /etc/httpd/conf/gonfy.conf
2026/09/26 17:53:56 dry-run: would run systemctl [enable httpd]
2026/09/26 17:53:56 dry-run: would run systemctl [start httpd]
2026/09/26 17:53:56 dry-run: would update /usr/local/sbin/gonfy-rotate
2026/09/26 17:53:56 dry-run: would ensure unit dir /etc/systemd/system
2026/09/26 17:53:56 dry-run: would update /etc/systemd/system/gonfy-rotate.service
2026/09/26 17:53:56 dry-run: would update /etc/systemd/system/gonfy-rotate.timer
2026/09/26 17:53:56 dry-run: would run systemctl [daemon-reload]
2026/09/26 17:53:56 dry-run: would run systemctl [enable gonfy-rotate.timer]
2026/09/26 17:53:56 dry-run: would run systemctl [start gonfy-rotate.timer]
2026/09/26 17:53:56 dry-run: would update /etc/sysctl.d/90-gonfy-web.conf
2026/09/26 17:53:56 dry-run: would run sysctl -p /etc/sysctl.d/90-gonfy-web.conf
summary: 2 ok, 0 changed, 0 skipped, 19 would-change
  would-change Group[gonfy-deploy]
  would-change User[gonfy-deploy]
  would-change Directory[/var/www/gonfy]
  would-change Directory[/var/www/gonfy/htdocs]
  would-change File[/var/www/gonfy/htdocs/gonfy.txt]
  would-change File[/var/www/gonfy/htdocs/index.html]
  would-change File[/etc/httpd/gonfy.htpasswd]
  would-change ConfigSet[httpd]
  would-change ConfigSetMember[httpd/httpd.conf]
  would-change ConfigSetMember[httpd/gonfy.conf]
  would-change Service[httpd]
  would-change File[/usr/local/sbin/gonfy-rotate]
  would-change File[/etc/systemd/system/gonfy-rotate.service]
  would-change File[/etc/systemd/system/gonfy-rotate.timer]
  would-change DaemonReload[system]
  would-change Timer[gonfy-rotate.timer]
  would-change SystemdTimer[gonfy-rotate]
  would-change File[/etc/sysctl.d/90-gonfy-web.conf]
  would-change Command[sysctl-web]
applied stdin+blobs (35 ops)
pushed push (35 ops) to paul@fe1.lan
```

`Package[httpd]` and `Package[httpd-tools]` are the two `ok` ops: httpd is
already installed. The firewall block does not show up, because `fe1` has
no `/usr/bin/firewall-cmd`. The preview also installed gonf on `fe1`
(`syncing gonf binary`); use `-preview` for a look that installs nothing.

## Push to the cluster

`cluster` records the plan once and pushes it to every front end. `-j 1`
keeps the output in host order:

```text
$ ./gonf cluster -j 1 frontends frontend
2026/09/26 17:53:57 created directory /var/www/gonfy
2026/09/26 17:53:57 created directory /var/www/gonfy/htdocs
2026/09/26 17:53:57 updated /var/www/gonfy/htdocs/gonfy.txt
2026/09/26 17:53:57 updated /var/www/gonfy/htdocs/index.html
2026/09/26 17:53:57 updated /etc/httpd/gonfy.htpasswd
2026/09/26 17:53:57 config set httpd: published /etc/httpd/conf/httpd.conf
2026/09/26 17:53:57 config set httpd: published /etc/httpd/conf/gonfy.conf
2026/09/26 17:53:57 systemctl [enable httpd]
2026/09/26 17:53:57 systemctl [start httpd]
2026/09/26 17:53:57 updated /usr/local/sbin/gonfy-rotate
2026/09/26 17:53:57 updated /etc/systemd/system/gonfy-rotate.service
2026/09/26 17:53:57 updated /etc/systemd/system/gonfy-rotate.timer
2026/09/26 17:53:57 systemctl [daemon-reload]
2026/09/26 17:53:57 systemctl [enable gonfy-rotate.timer]
2026/09/26 17:53:57 systemctl [start gonfy-rotate.timer]
2026/09/26 17:53:57 updated /etc/sysctl.d/90-gonfy-web.conf
2026/09/26 17:53:57 running Command[sysctl-web]: sysctl -p /etc/sysctl.d/90-gonfy-web.conf
summary: 2 ok, 19 changed, 0 skipped, 0 would-change
  changed Group[gonfy-deploy]
  changed User[gonfy-deploy]
  changed Directory[/var/www/gonfy]
  changed Directory[/var/www/gonfy/htdocs]
  changed File[/var/www/gonfy/htdocs/gonfy.txt]
  changed File[/var/www/gonfy/htdocs/index.html]
  changed File[/etc/httpd/gonfy.htpasswd]
  changed ConfigSet[httpd]
  changed ConfigSetMember[httpd/httpd.conf]
  changed ConfigSetMember[httpd/gonfy.conf]
  changed Service[httpd]
  changed File[/usr/local/sbin/gonfy-rotate]
  changed File[/etc/systemd/system/gonfy-rotate.service]
  changed File[/etc/systemd/system/gonfy-rotate.timer]
  changed DaemonReload[system]
  changed Timer[gonfy-rotate.timer]
  changed SystemdTimer[gonfy-rotate]
  changed File[/etc/sysctl.d/90-gonfy-web.conf]
  changed Command[sysctl-web]
applied stdin+blobs (41 ops)
[... fe2 and fe3 print the same, each after syncing its gonf binary ...]
pushed cluster-frontends (41 ops) to frontends (3/3 hosts)
```

Every front end now serves the site under its own name, and the status page
wants the password:

```text
$ ssh paul@fe2.lan curl -s localhost/
<!doctype html>
<html>
<head><title>Gonfy's lodge</title></head>
<body>
<h1>Welcome to Gonfy's lodge</h1>
<p>Served by fe2.</p>
</body>
</html>
$ ssh paul@fe2.lan curl -s -o /dev/null -w '%{http_code}\n' localhost/server-status
401
$ ssh paul@fe2.lan curl -s -u gonfy:lodge-keeper 'localhost/server-status?auto' | head -3
localhost
ServerVersion: Apache/2.4.37 (centos) OpenSSL/1.1.1g
ServerMPM: event
$ ssh paul@fe3.lan grep OnCalendar /etc/systemd/system/gonfy-rotate.timer
OnCalendar=*-*-* 04:20:00
```

`EachHost` gave `fe3` its own rotation time; `fe1` rotates at 04:00 and
`fe2` at 04:10.

## Run it again

```text
$ ./gonf cluster -j 1 frontends frontend
2026/09/26 17:54:00 skipping Command[sysctl-web]: no watched dependency changed
summary: 17 ok, 0 changed, 3 skipped, 0 would-change
applied stdin+blobs (41 ops)
2026/09/26 17:54:01 skipping Command[sysctl-web]: no watched dependency changed
summary: 17 ok, 0 changed, 3 skipped, 0 would-change
applied stdin+blobs (41 ops)
2026/09/26 17:54:01 skipping Command[sysctl-web]: no watched dependency changed
summary: 17 ok, 0 changed, 3 skipped, 0 would-change
applied stdin+blobs (41 ops)
pushed cluster-frontends (41 ops) to frontends (3/3 hosts)
```

Nothing changed on any front end. The skipped ops are the change-gated
ones, such as `sysctl-web`, whose file did not change.

## Drift on one front end

A woodpecker gets into `fe3` and edits the start page by hand. The next
push repairs `fe3` and leaves the other two alone:

```text
$ ssh paul@fe3.lan 'echo nibbled by a woodpecker > /var/www/gonfy/htdocs/index.html'
$ ./gonf cluster -j 1 frontends frontend_content
summary: 5 ok, 0 changed, 0 skipped, 0 would-change
applied stdin+blobs (8 ops)
summary: 5 ok, 0 changed, 0 skipped, 0 would-change
applied stdin+blobs (8 ops)
2026/09/26 17:54:02 updated /var/www/gonfy/htdocs/index.html
summary: 4 ok, 1 changed, 0 skipped, 0 would-change
  changed File[/var/www/gonfy/htdocs/index.html]
applied stdin+blobs (8 ops)
pushed cluster-frontends (8 ops) to frontends (3/3 hosts)
```

The site's files are read from `assets/web` each time your gonf records a
plan, so a new or edited page needs no rebuild: push again. Only a change
to the Go code needs `go build` (chapter 2).

## A broken config never goes live

Now a typo slips into `assets/web/gonfy.conf`: `Require valid-usr` instead
of `Require valid-user`. `ConfigSet` stages both files, runs `httpd -t`
against the staged pair and refuses to publish them:

```text
$ ./gonf cluster -j 1 frontends frontend_config
summary: 8 ok, 0 changed, 0 skipped, 0 would-change
apply: plan: apply line 15: config set httpd: validation by httpd failed, nothing published: exit status 1: validator output: AH00526: Syntax error on line 16 of /etc/httpd/conf/.gonf-configset-httpd+516180959/candidates/gonfy.conf: | Unknown Authz provider: valid-usr
pushed cluster-frontends (19 ops) to frontends (0/3 hosts)
cluster: cluster "frontends": fe1: chunk 0 (elevate=true): exit status 1
[exit status 1]
$ ssh paul@fe1.lan systemctl is-active httpd
active
$ ssh paul@fe1.lan grep Require /etc/httpd/conf/gonfy.conf
        Require all granted
        Require valid-user
```

`fe1` kept its working configuration and httpd kept running. Because `fe1`
failed, the cluster push stopped before it reached `fe2` and `fe3`
(`0/3 hosts`), so a broken change never spreads across the fleet. Fix the
typo and push again.

## What the plan carries

`plan -redacted` shows `frontend_config` and `frontend_logrotate`. The tasks
that `Needs` pulls in come first, and every task sits in its own cluster
guard:

```text
$ ./gonf plan -redacted frontend_config frontend_logrotate
{"op":"plan_preview","version":22,"id":"plan"}
{"op":"when_begin","id":"when.frontend_packages","elevate":true,"all":[{"fact":"hostname_contains","in":["fe1","fe2","fe3"]}]}
{"op":"package","id":"Package[httpd]","name":"httpd","elevate":true}
{"op":"package","id":"Package[httpd-tools]","name":"httpd-tools","elevate":true}
{"op":"when_end","elevate":true}
{"op":"when_begin","id":"when.frontend_account","elevate":true,"all":[{"fact":"hostname_contains","in":["fe1","fe2","fe3"]}]}
{"op":"user","id":"User[gonfy-deploy]","primary_group":"gonfy-deploy","home":"/var/www/gonfy","shell":"/sbin/nologin","system":true,"name":"gonfy-deploy","elevate":true}
{"op":"when_end","elevate":true}
{"op":"when_begin","id":"when.frontend_content","elevate":true,"all":[{"fact":"hostname_contains","in":["fe1","fe2","fe3"]}]}
{"op":"dir","id":"Directory[/var/www/gonfy]","path":"/var/www/gonfy","mode":"0755","owner":"gonfy-deploy","group":"gonfy-deploy","elevate":true}
{"op":"sync_dir","id":"Directory[/var/www/gonfy/htdocs]","path":"/var/www/gonfy/htdocs","mode":"0755","file_mode":"0644","owner":"gonfy-deploy","group":"gonfy-deploy","blob":"blobs/htdocs-8fd48dc8","source_dir":"assets/web/htdocs","prune":true,"elevate":true}
{"op":"when_end","elevate":true}
{"op":"when_begin","id":"when.frontend_config","elevate":true,"all":[{"fact":"hostname_contains","in":["fe1","fe2","fe3"]}]}
{"op":"file","id":"File[/etc/httpd/gonfy.htpasswd]","path":"/etc/httpd/gonfy.htpasswd","mode":"0640","owner":"root","group":"apache","content_b64":"[redacted]","has_content":true,"sensitive":true,"elevate":true}
{"op":"config_set","id":"ConfigSet[httpd]","name":"httpd","elevate":true,"members":[{"key":"httpd.conf","path":"/etc/httpd/conf/httpd.conf","content_b64":"IyBNYW5hZ2VkIGJ5IGdvbmYuIEdvbmZ5IG93bnMgdGhlIGZyb250IGRvb3Igb2YgdGhpcyBsb2RnZS4KU2VydmVyUm9vdCAiL2V0Yy9odHRwZCIKTGlzdGVuIDgwCkluY2x1ZGUgY29uZi5tb2R1bGVzLmQvKi5jb25mClVzZXIgYXBhY2hlCkdyb3VwIGFwYWNoZQpTZXJ2ZXJOYW1lIGxvY2FsaG9zdApTZXJ2ZXJBZG1pbiByb290QGxvY2FsaG9zdApFcnJvckxvZyAibG9ncy9lcnJvcl9sb2ciCkxvZ0xldmVsIHdhcm4KTG9nRm9ybWF0ICIlaCAlbCAldSAldCBcIiVyXCIgJT5zICViIFwiJXtSZWZlcmVyfWlcIiBcIiV7VXNlci1BZ2VudH1pXCIiIGNvbWJpbmVkCkN1c3RvbUxvZyAibG9ncy9hY2Nlc3NfbG9nIiBjb21iaW5lZApUeXBlc0NvbmZpZyAvZXRjL21pbWUudHlwZXMKQWRkRGVmYXVsdENoYXJzZXQgVVRGLTgKPERpcmVjdG9yeSAvPgogICAgQWxsb3dPdmVycmlkZSBOb25lCiAgICBSZXF1aXJlIGFsbCBkZW5pZWQKPC9EaXJlY3Rvcnk+CkluY2x1ZGUgIgBnb25mLW1lbWJlci1wYXRoOmdvbmZ5LmNvbmYAIgo=","mode":"0644","owner":"root","group":"0"},{"key":"gonfy.conf","path":"/etc/httpd/conf/gonfy.conf","content_b64":"IyBHb25meSdzIHNpdGU6IHN0YXRpYyBwYWdlcyBwbHVzIGEgcGFzc3dvcmQtcHJvdGVjdGVkIHN0YXR1cyBwYWdlLgo8VmlydHVhbEhvc3QgKjo4MD4KICAgIFNlcnZlck5hbWUgZ29uZnkuZXhhbXBsZS5vcmcKICAgIERvY3VtZW50Um9vdCAiL3Zhci93d3cvZ29uZnkvaHRkb2NzIgogICAgRGlyZWN0b3J5SW5kZXggaW5kZXguaHRtbAogICAgPERpcmVjdG9yeSAiL3Zhci93d3cvZ29uZnkvaHRkb2NzIj4KICAgICAgICBPcHRpb25zIE5vbmUKICAgICAgICBBbGxvd092ZXJyaWRlIE5vbmUKICAgICAgICBSZXF1aXJlIGFsbCBncmFudGVkCiAgICA8L0RpcmVjdG9yeT4KICAgIDxMb2NhdGlvbiAiL3NlcnZlci1zdGF0dXMiPgogICAgICAgIFNldEhhbmRsZXIgc2VydmVyLXN0YXR1cwogICAgICAgIEF1dGhUeXBlIEJhc2ljCiAgICAgICAgQXV0aE5hbWUgIkdvbmZ5J3MgbG9kZ2UiCiAgICAgICAgQXV0aFVzZXJGaWxlICIvZXRjL2h0dHBkL2dvbmZ5Lmh0cGFzc3dkIgogICAgICAgIFJlcXVpcmUgdmFsaWQtdXNlcgogICAgPC9Mb2NhdGlvbj4KPC9WaXJ0dWFsSG9zdD4K","mode":"0644","owner":"root","group":"0"}],"validators":[{"bin":"httpd","args":["-t","-f","\u0000gonf-member-path:httpd.conf\u0000"]}]}
{"op":"config_set_member","id":"ConfigSetMember[httpd/httpd.conf]","path":"/etc/httpd/conf/httpd.conf","name":"httpd","elevate":true,"member":"httpd.conf","deps":["ConfigSet[httpd]"]}
{"op":"config_set_member","id":"ConfigSetMember[httpd/gonfy.conf]","path":"/etc/httpd/conf/gonfy.conf","name":"httpd","elevate":true,"member":"gonfy.conf","deps":["ConfigSet[httpd]"]}
{"op":"service","id":"Service[httpd]","name":"httpd","reload":true,"if_changed":true,"watch":["ConfigSet[httpd]"],"elevate":true,"deps":["ConfigSet[httpd]"]}
{"op":"when_end","elevate":true}
{"op":"when_begin","id":"when.frontend_logrotate","elevate":true,"all":[{"fact":"hostname_contains","in":["fe1","fe2","fe3"]}]}
{"op":"file","id":"File[/usr/local/sbin/gonfy-rotate]","path":"/usr/local/sbin/gonfy-rotate","mode":"0755","owner":"root","group":"0","content_b64":"IyEvYmluL3NoCiMgUm90YXRlIEdvbmZ5J3MgaHR0cGQgbG9ncywga2VlcGluZyBvbmUgb2xkIGNvcHksIHRoZW4gbGV0IGh0dHBkIHJlb3BlbiB0aGVtLgpzZXQgLWV1CmNkIC92YXIvbG9nL2h0dHBkCmZvciBsb2cgaW4gYWNjZXNzX2xvZyBlcnJvcl9sb2c7IGRvCglpZiBbIC1mICIkbG9nIiBdOyB0aGVuCgkJbXYgLWYgIiRsb2ciICIkbG9nLjEiCglmaQpkb25lCmV4ZWMgc3lzdGVtY3RsIHJlbG9hZCBodHRwZAo=","has_content":true,"elevate":true}
{"op":"when_begin","id":"when.hostname:fe1","all":[{"fact":"hostname_contains","eq":"fe1"}]}
{"op":"systemd_timer","id":"SystemdTimer[gonfy-rotate]","name":"gonfy-rotate","command":"/usr/local/sbin/gonfy-rotate","on_calendar":"*-*-* 04:00:00","persistent":true,"description":"Rotate Gonfy's httpd logs","elevate":true}
{"op":"when_end"}
{"op":"when_begin","id":"when.hostname:fe2","all":[{"fact":"hostname_contains","eq":"fe2"}]}
{"op":"systemd_timer","id":"SystemdTimer[gonfy-rotate]","name":"gonfy-rotate","command":"/usr/local/sbin/gonfy-rotate","on_calendar":"*-*-* 04:10:00","persistent":true,"description":"Rotate Gonfy's httpd logs","elevate":true}
{"op":"when_end"}
{"op":"when_begin","id":"when.hostname:fe3","all":[{"fact":"hostname_contains","eq":"fe3"}]}
{"op":"systemd_timer","id":"SystemdTimer[gonfy-rotate]","name":"gonfy-rotate","command":"/usr/local/sbin/gonfy-rotate","on_calendar":"*-*-* 04:20:00","persistent":true,"description":"Rotate Gonfy's httpd logs","elevate":true}
{"op":"when_end"}
{"op":"when_end","elevate":true}
wrote redacted preview to stdout (31 ops, 1 secret-bearing; not a plan, cannot be applied)
```

- The password file is `sensitive`, so its content is `[redacted]`.
- The `config_set` op carries both files and the validator. `MemberPath`
  is still a placeholder; the destination fills it in with the staged path
  while validating and the live path afterwards.
- `Service[httpd]` has `reload` with `if_changed` and `watch`: httpd
  reloads only when the set changed.
- Each front end's timer is in a `when_begin` block on its host name, so
  one plan serves all three.

## Where to go from here

You have used most of gonf by now. The [reference](../reference.md) has
every option and sharp edge, and [chapter 15](15-troubleshooting.md) helps
when a push fails. Gonfy wishes you a tidy lodge.

Reference: [Inventory](../reference.md#inventory),
[RegisterMethods](../reference.md#registermethods),
[Per-host fragments](../reference.md#per-host-fragments),
[Package](../reference.md#package),
[Service and DaemonReload](../reference.md#service-and-daemonreload),
[SystemdTimer](../reference.md#systemdtimer),
[User](../reference.md#user), [Dir](../reference.md#dir),
[ConfigSet](../reference.md#configset),
[SecretFile](../reference.md#secretfile),
[Command](../reference.md#command).

---

← [15. When things go wrong](15-troubleshooting.md) · [Contents](README.md)
