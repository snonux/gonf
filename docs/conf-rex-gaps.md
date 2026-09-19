# Replacing `~/git/conf` Rex with gonf — gap audit

Refreshed 2026-09-19 against **gonf v0.13.0**; this release uses plan schema
15. This document is the
canonical plan for porting the [`~/git/conf`](https://codeberg.org/snonux/conf)
Rexfiles to gonf. Earlier revisions claimed gonf "still lacks Rex-style sudo/doas"
and that pkg/service/cron "fleet still needs transport" — both are **stale**:
fleet SSH transport (`push` / `cluster` / `fleet`) and the `Privileged()` +
`WithPrivilege(sudo|doas)` split apply shipped in v0.5.0–v0.6.0, and the
unattended-upgrades migration already runs on top of them (see
[The conf/gonf consumer](#the-confgonf-consumer)).

**Where things stand:** gonf is a full match for conf Rex on transport, privilege,
packages, services, cron, accounts, secrets, and file/line primitives. The
former feature gaps — a **secrets convention** (Rex `$secrets`), **custom
package repos** (Rex `PKG_PATH` env), and change-gated service restart (Rex
`on_change`) — are implemented in v0.13.0. Remaining work is consumer migration
plus a deliberately narrow existing-account update gap; account creation is
covered, while existing-account mutation remains explicit. Rich
templates are an ergonomics gap, not a capability gap — record-time Go code can
compute any content Perl closures could (see
[Templates](#templates-rich-data--closures)).

## Agreed operational scope

- **gonf is the configuration-management engine for conf going forward.** New
  fleet work lands as Go recipes under `~/git/conf/gonf/`, with SSH inventory in
  `gonf/cluster/cluster.go` and deploys via `./gonf.sh cluster <cluster> <tasks…>`
  (wrapper = `cd ./gonf && go run ./cmd/gonf`).
- **One consumer module per repository**, depending on `github.com/snonux/gonf`
  (both consumers currently pin v0.12.2; the downstream migration updates them
  to v0.13.0). Multi-Rexfile composition maps to Go packages +
  `RegisterMethods` + `Aggregate`, not to multiple Rexfiles.
- **Inventory lives in the consumer** (`Host` / `Cluster` / `Fleet` with
  `WithSSHUser` / `WithSSHPort` / `WithPrivilege` / `WithGOOS` / `WithGOARCH` /
  `WithValue`); the gonf library stays inventory-free.
- **Privilege is per host, declared once.** OpenBSD/NetBSD/FreeBSD hosts use
  `PrivilegeDoas` (login user `rex`/`paul`, never root SSH), Rocky hosts use
  `PrivilegeSudo`, r-nodes log in as root. Tasks needing root carry
  `RequiresRoot`; gonf splits every apply into a plain and an elevated chunk and
  wraps the elevated one (`doas gonf apply` / `sudo -n gonf apply`).
- **Porting order:** mechanical Rex tasks (pkg/file/service/cron) port first;
  template-heavy tasks port once the remaining gaps close or their templates are
  re-expressed as record-time Go. Rex stays the source of truth for not-yet-ported
  tasks — both systems may run against the same hosts in the meantime, but a task
  has exactly one owner once ported.
- **Library gaps are fixed in `~/git/gonf`**, tagged, then consumed via
  `go get github.com/snonux/gonf@v…` in both consumers (see conf
  [AGENTS.md](https://codeberg.org/snonux/conf) § "Moving a test from conf →
  gonf"). Tests for new APIs live in the gonf repo, not in consumer trees.

## Conf Rexfile inventory

| Path | Tasks | Role |
|------|-------|------|
| `Rexfile` | 0 | Aggregator: `require for <'*/Rexfile'>` + explicit `f3s/*` requires |
| `frontends/Rexfile` | 27 + `commons` | Main OpenBSD frontend fleet (blowfish, fishfinger; port 2; user `rex`; `sudo TRUE`; `parallelism 5`) |
| `f3s/garage/Rexfile` | 1 | Garage S3 config deploy to f0–f2 (user `paul`, no sudo, per-group `auth for`, `parallelism 1`) |
| `f3s/r-nodes/Rexfile` | 2 | Rocky k3s VMs r0–r2 (user `root`, `parallelism 3`): NFS-mount monitor units + persistent journal |
| `playground/Rexfile` | 1 | Rex cron API canary (blowfish) |

## Architecture

No mismatch remains — both engines converge on "record → transport → apply":

```text
Rex (conf):  rex task  →  SSH groups / sudo  →  remote file|pkg|service|run
gonf today:  gonf fleet →  Host/Fleet inventory → parallel GONF-PUSH/1 over ssh
             gonf push  →  one host → gonf apply -
             gonf plan  →  JSONL (+blobs) → gonf apply (manual ship)
             gonf task  →  RecordPlan+Apply locally (same engine)
```

One plan engine serves local and remote runs, so a recipe cannot diverge between
`./gonf.sh` (local) and `./gonf.sh cluster …` (pushed) — see [plan.md](plan.md).

## Current capability matrix

Status against every conf Rex primitive in v0.13.0 (plan schema 15):

| Conf Rex capability | gonf v0.13.0 | Status |
|---------------------|--------------|--------|
| `group x => 'h:2', …`, `user`, `parallelism 5` | `Host(name, WithSSHUser, WithSSHHost, WithSSHPort, WithSSHIdentity)` + `Cluster(name, hosts…)`, `cluster.Parallel(n)`; `gonf hosts`/`clusters`/`fleets` | **Done** |
| `sudo TRUE` / `auth for => group (user, sudo)` | `Task(…, Privileged())` (or `RequiresRoot`) + `Host(WithPrivilege(PrivilegeSudo|Doas|None))`; apply splits plain/elevated chunks; remote elevated chunk wraps `sudo -n gonf apply` / `doas gonf apply`; `-privilege=none` + elevated op refuses to push | **Done** |
| `run_task … on => connection->server` | `gonf push user@host t…`, `gonf cluster [-j N] [-host-timeout] <cluster> t…`, `gonf fleet <fleet> t…` (host-deduped), `PushHost` / `PushTo` / `PushClusterRun` / `PushFleetRun` in Go | **Done** |
| Parallel push resilience | per-cluster `.Parallel(n)` honored through fleets, `-j` override, per-host `-host-timeout` (default 10m), failing host cancels in-flight siblings fleet-wide, SIGINT/SIGTERM abort the push | **Done** |
| Remote binary lifecycle | push probes `gonf -plan-version`; missing/older gonf is cross-compiled (`WithGOOS`/`WithGOARCH`) and installed to `WithGonfPath` (default `/usr/local/bin/gonf`) with the host's privilege mode | **Done** |
| `pkg … ensure => present/absent` (pkg_add, pkg, pkgin, dnf) | `Package` / `NoPackage` with OS-auto-detected backends; `IsLatest` upgrade path (plan v10 `latest`) | **Done** |
| Custom repo / `PKG_PATH="https://pkgrepo…"` env on pkg_add | `Package(..., WithEnv(map[string]string{"PKG_PATH": …}), IsLatest)` passes the overlay to pkg probes and actions | **Done** (plan v15) |
| `service x, ensure => started` (rcctl / systemd / FreeBSD+NetBSD `service`) | `Service` / `NoService` auto-detect, `WithRestart` (restart once when already active), `WithReload`, `WithUser` (systemd) | **Done** |
| `on_change => sub { service 'x' => 'restart' }` (restart only when a file changed) | `OnChange(res…)`: Command runs only on a watched change; Service/Timer preserve state convergence but gate requested restart/reload; DaemonReload is gated too | **Done** (plan v11) |
| `template(...)` with arrays/loops/closures/per-server data | `.tmpl` sources render at destination apply with environment, `.Param`, structured `WithTemplateData`, and `.Gonf` host facts; Go still computes closures | **Done** |
| `$secrets->('path')` (`read_file './secrets/…'`) | `MustSecret` / `OptionalSecret` controller helpers | Covered |
| `append_if_no_such_line` | `WithLine` / `WithoutLine` (idempotent, mode-aware) | **Done** |
| `file …, ensure => 'absent'` | `NoFile` | **Done** |
| Rex `cron add => user, {…}` | `Cron` / `NoCron`: marker-managed per-user crontabs, full schedule fields, `WithCronEnv`, `WithCronUser` | **Done** (`@reboot` nice-to-have) |
| Raw crontab surgery via `run` (rsync, nsd_failover, pf rebuild root crontab) | superseded by `Cron` (marker-based, idempotent, no temp-file race) | **Done** (gonf is ahead) |
| Multi-Rexfile `require` composition | one Go module + `RegisterMethods(…, WithPrefix, WithCluster)` + `Aggregate`; proven by `~/git/conf/gonf` and `~/git/dotfiles/gonf` | **Done** |
| `adduser -batch _dserver … unless id _dserver`, `usermod -d` | additive-only `User` for creation-time group/class/home attributes; a guarded `Command("usermod", …)` remains necessary to converge the home of an already-existing OpenBSD account | **Done** for account creation; existing-account updates remain explicit |
| `/etc/login.conf.d` + `cap_mkdb` on change | `InstallFile` + `Command(..., OnChange(login))` | **Done** |
| garage pattern: write `/tmp` as login user, then `doas install … && doas service restart` | plain chunk `File(/tmp/…, owner login-user)` + `Command(…, WithElevate)` in the elevated chunk (wrapped `doas` by the host's `PrivilegeDoas`) | **Done** |
| Deferred `on_change` flag (`$restart = TRUE` … `service restart if $restart`) | `OnChange` supports multi-resource fan-in and carries ordering dependencies | **Done** |

## Implemented gaps and remaining migration work

Ordered by how often conf Rex uses them and how many ported tasks unblock.

### Implemented: change-gated restart/reload (`on_change`)

Rex restarts a service **only when a file changed** (11+ uses across httpd,
inetd, relayd, smtpd, nsd, gorum, pf, and both r-nodes tasks). gonf has the
plumbing (`IfChanged` watches `DependsOn` targets and dependency change notes)
but only `DaemonReload` implements it. (Garage restarts unconditionally every
run — no `on_change` there.)

`OnChange(resources...)` is the public, typed DSL for `Service`, `Timer`,
`Command`, and `DaemonReload`. It adds normal dependency ordering, supports
multi-resource fan-in, and records a schema-11 `if_changed`/`watch` gate:

```go
conf := InstallFile("/etc/relayd.conf", tplDir+"/relayd.conf", WithMode(0o600))
Service("relayd", WithRestart, OnChange(conf))             // restart only when conf changed

login := InstallFile("/etc/login.conf.d/daemon", src)
Command("cap_mkdb", List("/etc/login.conf"), OnChange(login)) // run once on change

units := InstallFile("/etc/systemd/system/nfs-mount-monitor.timer", src)
DaemonReload(OnChange(units…))
Timer("nfs-mount-monitor", WithRestart, OnChange(units))   // restart timer only when units changed
```

`OnChange` skips the command or holds the requested service/timer
restart/reload when no watched target reported a change; state enforcement
(started/enabled) still runs. Empty watches fail at registration, and empty,
dangling, or cross-privilege-chunk watches fail before a plan is written,
pushed, or applied. Legacy `IfChanged`/`WithWatch` remain compatible for
DaemonReload.

### Implemented: secrets convention (`$secrets`)

Rex reads tokens/keys from the gitignored `./secrets/` tree (goprecords token,
nsd `key.conf`, garage `rpc_secret`). gonf recipes can `os.ReadFile` today; a
tiny helper fixes the convention and failure mode:

```go
// reads <recipe-root>/secrets/<path…>, fails recording before push when
// missing/empty, and never logs the value.
key := MustSecret("var/nsd/etc/nsd_key.txt")
File("/var/nsd/etc/key.conf", WithContent(buildKeyConf(key)), …) // buildKeyConf = record-time Go
```

Document explicitly: secrets recorded via `WithContent` travel **in the clear
inside the plan JSONL** (temp dir locally, `GONF-PUSH/1` over SSH remotely) —
the same exposure Rex has shipping file content over SSH. No secret material
belongs in task names, descriptions, or host values (`WithValue` rows print in
`gonf hosts` listings).

### Implemented: custom package repos (`PKG_PATH`)

`dtail_install` / `gogios_install` install from `https://pkgrepo.f3s.buetow.org`
with `PKG_PATH=… pkg_add -u X || pkg_add X`; `pkgrepo_setup` appends the
`PKG_PATH` export to `/root/.profile`. The declarative form is:

```go
Package("dtail", WithEnv(map[string]string{
    "PKG_PATH": "https://pkgrepo.f3s.buetow.org/openbsd/7.8/packages/amd64/",
}), IsLatest)
```

`WithEnv` overlays the inherited environment for every package-manager probe
and mutation, and it survives plan recording/application. It is deliberately
general rather than a repository-specific abstraction: recipes can use the
native environment configuration of dnf, pkg_add, pkg, or pkgin.

### 4. Cron `@reboot` (nice-to-have)

`freebsd.Cron` works around the missing `@reboot` with an hourly stamp-gated
script ("boot catch-up is the next hourly tick"). Proposal: `WithSpec("@reboot")`
or a `WithReboot()` option that emits `@reboot` in the marker block. Low value —
the workaround holds.

### Implemented: `User` resource

`dtail`, `gogios`, and `gorum` create service users (`adduser -batch … unless
id …` + `usermod -d`). The public resource is:

```go
User("_dserver", WithLoginClass("nologin"), WithPrimaryGroup("_dserver"), WithHome("/var/run/dserver"))
```

`User` is deliberately additive-only: it creates only missing accounts/groups
and adds missing supplementary memberships, without deleting or rewriting
existing accounts. In particular, `WithHome` is a creation attribute: it does
**not** converge the home directory of an account that already exists. See
[user.md](user.md).

The three OpenBSD frontend ports should declare the creation attributes that
the Rexfiles use: `_dserver` and `_gorum` need
`WithPrimaryGroup(name)`, `WithLoginClass("nologin")`, and `WithHome`; `_gogios`
needs `WithPrimaryGroup(name)` and `WithHome` (no nologin class in Rex). To
preserve Rex's existing-account `usermod -d` behavior during migration, use a
separate, explicitly OpenBSD-specific guarded command after the `User`
resource:

```go
account := User("_dserver",
    WithPrimaryGroup("_dserver"),
    WithLoginClass("nologin"),
    WithHome("/var/run/dserver"),
)
Command("usermod", List("-d", "/var/run/dserver", "_dserver"),
    DependsOn(account),
    Unless("sh", List("-c", `awk -F: '$1 == "_dserver" && $6 == "/var/run/dserver" { found = 1 } END { exit !found }' /etc/passwd`)),
)
```

That command is intentionally a migration-local compatibility step, rather
than an implicit mutation by `User`. A future convergent existing-account
update API is a narrow remaining gonf gap and must define per-platform safety
and idempotency before it is added; until then, use an explicit guarded
`Command` only when a port must preserve a Rex update.

### Templates (rich data + closures)

Rex templates embed Perl (loops over `@acme_hosts`/`@f3s_hosts`, per-server
`$hostname`, closures like `$ipv4address`, secret interpolation). gonf uses Go
`text/template`, so it cannot render those Perl templates as-is; recipes must
translate their logic to Go data and templates.

The Go-native replacement is **record-time content computation**: recipes are Go,
so anything the Perl template computed can be computed while recording — shared
arrays as package vars, per-host values via `MustHostValue` + `WhenHostname(host, …)`
fragments, secrets via `MustSecret` / `OptionalSecret`. The one-line `myname.tpl` becomes a Go
expression; zone-file loops become `EachKV`/`for` over the zone list emitting one
`InstallFile` per zone. This costs more lines than Rex but lives in one language
and is fully type-checked.

`WithTemplateData(any)` carries JSON-compatible maps, slices, and structs on
the file op. Templates render at destination apply with stable string helpers,
strict missing-key errors, and live destination facts under `.Gonf`. This covers
large config loops and per-host values while keeping closures in Go.

## The conf/gonf consumer

The unattended-upgrades migration is complete for all four OS groups and proves
the composition pattern (`RegisterMethods` + `WithCluster` + per-host
`WithValue`):

| Prefix / cluster | Tasks | Notes |
|------------------|-------|-------|
| `frontends_*` / `frontends` (blowfish, fishfinger; doas; openbsd/amd64) | ping, script, services, cron, newsyslog | `OptsPing` opts out of the struct-level `RequiresRoot` |
| `pis_netbsd_*` / `netbsd-pis` (pi0, pi1; doas; netbsd/arm64) | script, services, cron, newsyslog | `WhenHostname(ClusterHosts())` fragments |
| `rocky_*` / `rocky-all` (pi2, pi3, r0–r2; sudo; linux) | gonf_link, packages, script, stamp_dir, units, logrotate | `SystemdTimer` per-host `OnCalendar` |
| `freebsd_*` / `freebsd-hosts` (f0–f3; doas; freebsd/amd64) | packages, script, services, stamp_dir, cron, newsyslog | hourly minute via `WithValue`; `@reboot` workaround |

Inventory invariants (from `cluster.go`): pi/r/f hostnames are substrings of the
live OS hostnames so `WhenHostname("piN")` / `WhenHostname("rN")` / `WhenHostname("fN")`
match; LAN hosts pin `WithSSHPort(22)` because `~/.ssh/config` maps
`*.buetow.org` to port 2.

## Implementation ordering

1. **Complete: `OnChange` on Service / Timer / Command / DaemonReload**
   (schema 11) — unblocks httpd, inetd, relayd, smtpd, nsd, gorum, pf, r-nodes
   monitor + journal, garage restart, and login.conf `cap_mkdb`.
2. **Complete: `MustSecret` / `OptionalSecret` + plan-secrecy doc note** — unblocks goprecords_upload,
   nsd key.conf, garage_deploy (which can already ship with `os.ReadFile`).
3. **Complete: `WithEnv` on Package** — unblocks dtail_install, gogios_install,
   complements `pkgrepo_setup`.
4. **Port mechanical frontends tasks** (no feature deps): base pkgs, hosts_wg,
   uptimed, acme_invoke, pkgrepo_setup, foostats, ircbouncer, nsd_failover,
   cron_test canary, gorum_install, gogios user/dirs/cron scaffolding.
5. **Port template-heavy tasks** with record-time Go content or `WithTemplateData`:
   base/myname,
   gemtexter, acme, httpd, inetd, relayd, smtpd, nsd zones, gogios.json, pf —
   each using the completed restart wiring where needed.
6. **Port f3s tasks**: garage_deploy and r-nodes nfs_mount_monitor +
   persistent_journal, using the completed secret and change-gate APIs.
7. **Nice-to-have** (only if consumers still feel the pain): cron `@reboot`.

Steps 1–3 are gonf-library work (tests + plan bump + docs); steps 4–6 are
conf-consumer work tagged to a gonf release; each port flips task ownership from
Rex to gonf (comment out the Rex task or delete it once the live deploy
converges).

## Rex task mapping

Every conf Rex task and its gonf fate. "consumer" = already lives in
`~/git/conf/gonf`. Feature codes: [on-change], [secrets], [pkg-path], and
[templates] — see gaps above.

Privilege note: the frontends tasks write root-owned files (`/etc/*`,
`/usr/local/*`, `/root/.profile`) while the SSH login is `rex`, so those tasks
carry `RequiresRoot` / `Privileged()` exactly like the consumer's unattended
structs — `File` / `InstallFile` have no per-op elevate; only `Command(…,
WithElevate)` does. The garage task is the deliberate exception: its `File`
lands in `/tmp` owned by the login user (plain chunk), only the two
`install`/`service` commands are elevated.

### frontends/Rexfile (target: gonf cluster `frontends`, tasks `frontends_*`)

| Rex task | gonf port | Status / needs |
|----------|-----------|----------------|
| `commons` (run_task aggregator) | `Aggregate("frontends", …, "^frontends_")` | **Done** (consumer). Note: the aggregate is a superset — Rex `commons` runs an 18-task subset, while every `frontends_*` method joins the aggregate; keep install-only helpers (`*_install`, `cron_test` canary) out of the pattern or accept them running on each deploy |
| `id`, `dump_info` | — | **Excluded** (interactive diagnostics; `Command("id", nil)` ad hoc) |
| `base` (6× pkg present; `pkg_scripts="…"` append to `/etc/rc.conf.local` (znc added on the ircbouncer host); `touch /etc/rc.local`; `/etc/myname` from closure template; `tmux-edit-send` source file) | `Package` ×6 + `File(WithLine)` + `File` + Go-computed content + `InstallFile`; task split `frontends_base`, `frontends_myname` | Needs [templates] only for ergonomics; per-host `myname` via `WhenHostname` + record-time content. **To do** |
| `hosts_wg` (append `etc/hosts.wg.append` lines, skip comments/blanks) | `File("/etc/hosts", WithLine(each))`, lines read at record time | **To do** (no new features) |
| `uptimed` | `Package("uptimed")` + `Service("uptimed")` | **To do** (no new features) |
| `goprecords_upload` (token from secrets → `/etc/goprecords-upload.token` 0600; script; `daily.local` append; old script absent; old daily.local line stripped) | `Package("curl")` + `MustSecret` + `File` + `InstallFile` + `File(WithLine)` + `WithoutLine` + `NoFile` | **To do** (no missing feature) |
| `rsync` (pkg; rsyncd.conf + rsync.sh templates; root crontab rebuilt via temp files + run) | `Package("rsync")` + 2× Go-computed template content + `Cron("rsync", WithCommand("-ns /usr/local/bin/rsync.sh"), WithMinute("*/5"))` | Cron replaces the raw crontab surgery (user defaults to root); OpenBSD cron job flags (`-ns`) ride in the verbatim command field. Needs [templates→Go]. **To do** |
| `gemtexter` (template → `/usr/local/bin/gemtexter.sh`; daily.local append) | `InstallFile` + `File(WithLine)` | Needs [templates→Go]. **To do** |
| `acme` (2 templates over `@acme_hosts`; daily.local append) | 2× `File`/`InstallFile` + `File(WithLine)` | Needs [templates→Go]. **To do** |
| `acme_invoke` (run acme.sh every deploy) | `Command("/usr/local/bin/acme.sh", nil)` without guards (runs every apply — matches Rex) | **To do** (no new features) |
| `httpd` (rc.conf.local flags append; httpd.conf template **restart-on-change**; htdocs dirs; fallback page + health-check `index.txt` template; service) | privileged task: `File(WithLine)` + 2× `File` (templates) + `EnsureDir` ×3 + `InstallFile` + `Service("httpd", WithRestart, OnChange(conf))` | **To do** (template translation; no missing feature) |
| `inetd` (flags append; login.conf.d/inetd; inetd.conf restart-on-change; service) | `File(WithLine)` + 2× `InstallFile` + `Service("inetd", WithRestart, OnChange(conf))` | **To do** (no missing feature) |
| `relayd` (flags append; login.conf.d/daemon + `cap_mkdb` on change; relayd.conf 0600 restart-on-change; service; daily.local append) | `File(WithLine)` ×2 + `InstallFile` ×2 + `Command("cap_mkdb", …, OnChange(login))` + `Service("relayd", WithRestart, OnChange(conf))` + `File(WithLine)` | **To do** (template translation; no missing feature) |
| `smtpd` (aliases → `newaliases` on change; virtualdomains/users; 3 reject lists; smtpd.conf restart-on-change; service) | `InstallFile` ×7 + `Command("newaliases", …, OnChange(aliases))` + `Service("smtpd", WithRestart, OnChange(conf))` | **To do** (no missing feature) |
| `nsd` (flags append; key.conf from secret; nsd.conf.master; per-zone templates; zone removals; restart-if-changed; service) | `File(WithLine)` + `MustSecret` + `File` + `for` loop over zones emitting one `File` per zone + `NoFile` ×removed + `Service("nsd", WithRestart, OnChange(configs))` | **To do** (template translation; no missing feature) |
| `nsd_failover` (script + root crontab via run) | `InstallFile` + `Cron("nsd_failover", WithCommand("-ns /usr/local/bin/dns-failover.ksh"), WithMinute("*"))` | **To do** (no new features; Cron replaces the temp-file crontab race, `-ns` in the command field, root is the default user) |
| `dtail_install` (remove stray binaries; `PKG_PATH=… pkg_add -u dtail ‖ pkg_add dtail`) | `Command` cleanup probes + `Package("dtail", WithEnv(map[string]string{"PKG_PATH": …}), IsLatest)` | **To do** (no missing package feature) |
| `dtail` (dtail_install + adduser `_dserver` + `usermod -d` + daily.local appends + service) | depends on dtail port + `User("_dserver", WithPrimaryGroup("_dserver"), WithLoginClass("nologin"), WithHome("/var/run/dserver"))` + guarded OpenBSD `Command("usermod", List("-d", "/var/run/dserver", "_dserver"), DependsOn(account))` for pre-existing accounts + `File(WithLine)` ×2 + `Service("dserver")` | **To do** (creation supported; retain the explicit guarded home update) |
| `pkgrepo_setup` (`PKG_PATH` export appended to `/root/.profile`) | `File("/root/.profile", WithLine(export PKG_PATH=…))` | **To do** (no new features) |
| `gogios_install` (uname branch: OpenBSD custom-repo `pkg_add -u ‖ install`; FreeBSD branch is dead code) | `Package("gogios", WithEnv(map[string]string{"PKG_PATH": …}), IsLatest)` (frontends are all OpenBSD; branch collapses) | **To do** (no missing package feature) |
| `gogios` (pkg ×2; adduser `_gogios`; dirs; gogios.json template over 3 arrays; `check_shuriken_age` sourced from `~/git/shuriken.sh`; `_gogios` crontab from template; rc.local appends) | `Package` ×2 + `User("_gogios", WithPrimaryGroup("_gogios"), WithHome("/var/run/gogios"))` + guarded OpenBSD `Command("usermod", List("-d", "/var/run/gogios", "_gogios"), DependsOn(account))` for pre-existing accounts + `EnsureDir` ×2 + Go-computed `gogios.json` + `InstallFile` (absolute source outside repo — supported) + `Cron("gogios_check", WithCronUser("_gogios"), …)` + `File(WithLine)` ×2 | Needs [templates→Go]; retain the explicit guarded home update. **To do** |
| `cron_test` (Rex cron canary, `_gogios` user) | `Cron("frontends_cron_test", WithCronUser("_gogios"), …)` | **To do** (canary; low priority) |
| `gorum_install` (source file; Rexfile has malformed owner/group attrs — fix at port) | `InstallFile("/usr/local/bin/gorum", …)` | **To do** (no new features; note the Rexfile attribute-syntax bug) |
| `gorum` (adduser `_gorum`; gorum.json + rc.d/gorum restart-on-change; `/var/run/gorum`; service) | `User("_gorum", WithPrimaryGroup("_gorum"), WithLoginClass("nologin"), WithHome("/var/run/gorum"))` + guarded OpenBSD `Command("usermod", List("-d", "/var/run/gorum", "_gorum"), DependsOn(account))` for pre-existing accounts + 2× `File` (templates) + `EnsureDir` + `Service("gorum", WithRestart, OnChange(configs))` | **To do** (template translation; retain the explicit guarded home update) |
| `foostats` (copies scripts from `~/git/foostats`; installs; dirs; daily.local; 5× p5-* pkg; newsyslog.conf) | `InstallFile` (source directly from `~/git/foostats/…`) + `EnsureDir` ×2 + `File(WithLine)` + `Package` ×5 + `InstallFile` | **To do** (no new features) |
| `ircbouncer` (pkg znc; service; fishfinger only) | `Package("znc")` + `Service("znc")` as `frontends_ircbouncer` (`WhenHostname("fishfinger")`) | **To do** (no new features) |
| `pf` (pf.conf restart-on-change → `pfctl -f`; `/var/node_exporter` dir; exporter script; root cron (`-ns`); `rcctl set node_exporter flags`; restart) | privileged task (task-level elevation covers every op): `File` (template) + `EnsureDir` + `InstallFile` + `Cron("pf_labels", WithCommand("-ns …"))` + `Command("rcctl", …)` ×2 + `Command("pfctl", List("-f", "/etc/pf.conf"), OnChange(conf))` | **To do** (template translation; no missing feature) |

### f3s/garage/Rexfile (target: new gonf cluster, e.g. `garage` on f0–f2)

| Rex task | gonf port | Status / needs |
|----------|-----------|----------------|
| `garage_deploy` (secret substitution into `garage.$suffix.toml`; `/tmp` staging write as `paul` then removed; `doas install -o root -g garage -m 640`; `doas service garage restart`; per-group auth, parallelism 1) | `Host("f0"…"f2", WithSSHUser("paul"), WithPrivilege(PrivilegeDoas), WithGOOS("freebsd"))` cluster `.Parallel(1)`; task: `File("/tmp/garage.toml…", WithContent(Go-substituted from MustSecret), WithOwner("paul"), WithMode(0o600))` + `Command("install", …, WithElevate)` + `Command("service", …, WithElevate)` (elevated chunk wrapped `doas`) + `Command("rm", List("-f", tmp), WithElevate)` — op targets persist after apply, so Rex's `rm -f $tmp` needs an explicit command | **To do** (no missing feature; Rex intentionally restarts every run) |

### f3s/r-nodes/Rexfile (target: gonf cluster `rocky-k3s` — r0–r2 already registered)

| Rex task | gonf port | Status / needs |
|----------|-----------|----------------|
| `nfs_mount_monitor` (9 files + 5 dirs, root; change flag → one `daemon-reload` + timer restart; enable+start 3 units) | `EnsureDir` ×5 + `InstallFile` ×9 + `DaemonReload(OnChange(units))` + `Timer("nfs-mount-monitor", WithRestart, OnChange(units))` + `Service("nfs-shutdown-marker")` + `Service("k3s-nfs-drain")` | **To do** (no missing feature) |
| `persistent_journal` (journald drop-in; `/var/log/journal` 2755; change → tmpfiles + restart journald; `journalctl --flush`) | `EnsureDir` ×2 + `InstallFile` + `DaemonReload(OnChange(drop-in))` + `Command("systemd-tmpfiles", …, OnChange(drop-in))` + `Service("systemd-journald", WithRestart, OnChange(drop-in))` + `Command("journalctl", List("--flush"))` | **To do** (no missing feature) |

### playground/Rexfile

| Rex task | gonf port | Status / needs |
|----------|-----------|----------------|
| `openbsd_cron_test` (cron canary) | covered by the consumer canary pattern (`frontends_ping` + `Cron`); no separate port | **Excluded** (superseded) |

## Exclusions

- `conf/rcm` — unrelated to Rex.
- `id` / `dump_info` diagnostics tasks.
- `playground/Rexfile` — experiment; superseded by the consumer canary.
- Unused NetBSD/FreeBSD dserver + nsd slave templates in `frontends/etc`
  (`dserver-netbsd.tpl`, `dserver-freebsd.tpl`, `nsd.conf.slave.tpl`, …).
- Dead code: the FreeBSD branch of `gogios_install` (frontends group is
  OpenBSD-only) — collapse at port time.
- Interactive sudo/doas passwords — out of scope for gonf; conf hosts use
  passwordless doas/sudo already.
- `gorum` stays unported while it is disabled in Rex `commons` (`# run_task
  'gorum'`); port together with re-enablement.
- The Rex `sudo TRUE` + `user rex` global settings and per-Rexfile
  `parallelism` — replaced by per-Host `WithPrivilege` / per-Cluster
  `Parallel(n)`; there is no global gonf equivalent by design.
- Per-host template rendering on the *destination* (`{{.Param}}` derives from the
  file path) — conf's per-server data moves to record-time per-host fragments
  instead of trying to make `.tmpl` richer.

## Acceptance criteria

For this document:

- Every capability row names the gonf API that exists today (verified against
  v0.13.0, plan schema 15) — no "fleet needs transport" or
  missing-feature claims survive.
- All five Rexfiles are inventoried and every task appears exactly once in the
  mapping with a status (consumer / to do / excluded) and feature codes.
- The gap list matches the proposed-API list and the implementation ordering.

For each future implementation task created from this plan (library steps 1–3 and
consumer ports 4–6):

- gonf library: `go build ./...`, `go test -race -shuffle=on -count=1 ./...`,
  `go vet ./...`, `go tool staticcheck ./...`, `gofmt -l .`, and
  `errcheck ./...` all clean; plan-code changes update the plan docs and bump
  `CurrentVersion` with refusal semantics at the header gate.
- Compatibility gate: a temporary `go.work` referencing `~/git/gonf`,
  `~/git/dotfiles/gonf`, and `~/git/conf/gonf` (without editing client
  `go.mod` files) builds both consumers: `go test ./...`,
  `go build ./cmd/gonf`, `go run ./cmd/gonf -list`, plus non-applying `-n`
  task runs for the dotfiles `home_tmux` / `home_systemd_user` and
  the conf `frontends_services`, `rocky_units`, `freebsd_services`,
  `pis_netbsd_services` tasks and every task the work touched.
- Live proof per ported task: `./gonf.sh cluster <cluster> <task>` (or
  `-n` first) converges the host the way the Rex task did, and the Rex task is
  retired from its Rexfile in the same change.
- Consumer module bumps: tag the gonf release first, then
  `go get github.com/snonux/gonf@v…` in both consumers.

## Related

- Consumer that fits gonf today: `~/git/dotfiles/gonf` (Fedora laptop) and
  `~/git/conf/gonf` (this plan's first migration wave).
- `f3s/garage/Justfile` `deploy` still calls `rex garage_deploy`; it switches to
  `./gonf.sh cluster garage garage_deploy` once that task ports.
- Feature docs: [README.md](README.md) (index), [plan.md](plan.md) (transport +
  privilege split), [package.md](package.md), [service.md](service.md),
  [cron.md](cron.md), [helpers.md](helpers.md), [options.md](options.md).
