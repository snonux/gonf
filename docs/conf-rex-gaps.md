# Replacing `~/git/conf` Rex with gonf — gap audit

Refreshed 2026-09-22 against **gonf v0.15.0**; this release uses plan schema
21. This document is the
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
`on_change`) — are implemented since v0.13.0. Remaining work is consumer migration
plus a deliberately narrow existing-account update gap; account creation is
covered, while existing-account mutation remains explicit. Rich
templates are an ergonomics gap, not a capability gap — record-time Go code can
compute any content Perl closures could (see
[Templates](#templates-rich-data--closures)).

## Agreed operational scope

- **gonf is the configuration-management engine for conf going forward.** New
  fleet work lands as Go recipes under `~/git/conf/gonf/`, with SSH inventory in
  `gonf/cluster/cluster.go` and deploys via `./gonf.sh cluster <cluster> <tasks…>`
  (the wrapper resolves its own checkout, changes into `gonf/` so the
  `gonf/secrets` root resolves, and passes its arguments through verbatim;
  it works from any directory, conf 63e83a8).
- **One consumer module per repository**, depending on `github.com/snonux/gonf`
  (dotfiles and conf both pin v0.15.0; any later consumer upgrade is a
  deliberate compatibility change). Multi-Rexfile composition maps to Go
  packages + `RegisterMethods` + `Aggregate` / `AggregateTasks` (in conf's
  `gonf/tasks/tasks.go`), not to multiple Rexfiles.
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
| `frontends/Rexfile` | 27 + `commons` (legacy, not deployed) | Main OpenBSD frontend fleet (blowfish, fishfinger; port 2; user `rex`; `sudo TRUE`; `parallelism 5`) |
| `f3s/garage/Rexfile` | retired | Garage S3 config now deploys through `gonf garage_config` to f0–f2 (user `paul`, doas, parallelism 1) |
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

Status against every conf Rex primitive in v0.15.0 (plan schema 21):

| Conf Rex capability | gonf v0.15.0 | Status |
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
| `$secrets->('path')` (`read_file './secrets/…'`) | `MustSecret` / `OptionalSecret` controller helpers over the default file provider; `SetSecretProvider` / `ResolveSecret` for another store ([secrets.md](secrets.md)) | Covered |
| `append_if_no_such_line` | `WithLine` / `WithoutLine` (idempotent, mode-aware) | **Done** |
| `file …, ensure => 'absent'` | `NoFile` | **Done** |
| Rex `cron add => user, {…}` | `Cron` / `NoCron`: marker-managed per-user crontabs, full schedule fields, `WithCronEnv`, `WithCronUser` | **Done** (`@reboot` nice-to-have) |
| Raw crontab surgery via `run` (rsync, nsd_failover, pf rebuild root crontab) | superseded by `Cron` (marker-based, idempotent, no temp-file race) | **Done** (gonf is ahead) |
| Multi-Rexfile `require` composition | one Go module + `RegisterMethods(…, WithPrefix, WithCluster)` + `Aggregate`; proven by `~/git/conf/gonf` and `~/git/dotfiles/gonf` | **Done** |
| `adduser -batch _dserver … unless id _dserver`, `usermod -d` | additive `User` for creation-time group/class/home attributes; `WithManageHome` converges an existing account's passwd home field | **Done**: account creation in v0.14.0, `WithManageHome` in v0.15.0 (plan v19). conf pins v0.15.0 and its frontend service accounts use it (task s52, conf 7bc33b3); the guarded `usermod -d` command is gone |
| `/etc/login.conf.d` fragment (Rex relayd also ran `rm -f /etc/login.conf.db && cap_mkdb`; Rex inetd ran none) | `LoginClass(class, src)`: the fragment inside an OpenBSD-only plan requirement (schema 20) plus removal of a stale `<class>.db`; no `cap_mkdb`, which never reads fragments (see [login-class.md](login-class.md)). conf's inetd and relayd use it (task t52, conf 7ec3015); their former `cap_mkdb` rebuild, a no-op for fragment changes, is gone | Implemented in core (v0.15.0); native OpenBSD verification pending |
| Garage config deployment | `RequiresRoot` task + direct `InstallFile("/usr/local/etc/garage.toml", …, root:garage, 0640, WithTemplateData(...))` + `Service("garage", WithRestart, OnChange(config))`; the host's `PrivilegeDoas` wraps the one privileged chunk | **Done** |
| Deferred `on_change` flag (`$restart = TRUE` … `service restart if $restart`) | `OnChange` supports multi-resource fan-in and carries ordering dependencies | **Done** |

## Implemented gaps and remaining migration work

Ordered by how often conf Rex uses them and how many ported tasks unblock.

### Implemented: change-gated restart/reload (`on_change`)

Rex restarts a service **only when a file changed** (11+ uses across httpd,
inetd, relayd, smtpd, nsd, gorum, pf, and both r-nodes tasks). gonf implements
the same change gate for `Service`, `Timer`, `Command`, and `DaemonReload`:
`OnChange` records dependency ordering and watched change reports. The retired
Garage Rex task restarted unconditionally; `garage_config` now uses that gate.

`OnChange(resources...)` is the public, typed DSL for `Service`, `Timer`,
`Command`, and `DaemonReload`. It adds normal dependency ordering, supports
multi-resource fan-in, and records a schema-11 `if_changed`/`watch` gate:

```go
conf := InstallFile("/etc/relayd.conf", tplDir+"/relayd.conf", WithMode(0o600))
Service("relayd", WithRestart, OnChange(conf))             // restart only when conf changed

login := InstallFile("/etc/login.conf.d/daemon", src)
Command("cap_mkdb", List("/etc/login.conf"), OnChange(login)) // run once on change
// For /etc/login.conf.d fragments prefer LoginClass("daemon", src): this cap_mkdb
// never reads the fragment, so it only makes sense for edits of /etc/login.conf itself.

units := InstallFile("/etc/systemd/system/nfs-mount-monitor.timer", src)
DaemonReload(OnChange(units…))
Timer("nfs-mount-monitor", WithRestart, OnChange(units))   // restart timer only when units changed
```

`OnChange` skips the command or holds the requested service/timer
restart/reload when no watched target reported a change; state enforcement
(started/enabled) still runs. Empty watches fail at registration, and empty,
dangling, or cross-privilege-chunk watches fail before a plan is written,
pushed, or applied. Legacy `IfChanged`/`WithWatch` remain compatible for
DaemonReload (after v0.15.0 they feed the same gate as `OnChange`; see
[options.md](options.md#change-gate-changes-after-v0150) for the
unreleased pre-1.0 changes).

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

The helpers now resolve through a typed secret-provider contract (package
`secret`, see [secrets.md](secrets.md)): the default `secret.FileProvider`
keeps this `./secrets/` convention byte for byte, another store is configured
once with `SetSecretProvider`, and `OptionalSecret` suppresses only a
not-found secret. A missing `secrets/` directory itself is an error for both
helpers, no longer "every secret is missing". The provider does not change
what reaches the plan.

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

`User` is additive by default: it creates only missing accounts/groups and
adds missing supplementary memberships, without deleting or rewriting existing
accounts. `WithHome` alone is a creation attribute: it does **not** converge
the home of an account that already exists. See [user.md](user.md).

The OpenBSD frontend ports declare the creation attributes that
the Rexfiles use: `_dserver` and `_gorum` need
`WithPrimaryGroup(name)`, `WithLoginClass("nologin")`, and `WithHome`; `_gogios`
needs `WithPrimaryGroup(name)` and `WithHome` (no nologin class in Rex). To
preserve Rex's existing-account `usermod -d` behavior, add the explicit
`WithManageHome` opt-in (released in gonf v0.15.0; plan schema v19):

```go
account := User("_dserver",
    WithPrimaryGroup("_dserver"),
    WithLoginClass("nologin"),
    WithHome("/var/run/dserver"),
    WithManageHome,
)
```

`WithManageHome` rewrites only the passwd home field (`usermod -d` on
OpenBSD/NetBSD, `usermod --home` on Rocky, `pw usermod -d` on FreeBSD, never
with the move flag) when it differs from `WithHome`. It never moves, creates,
or chowns the directory, and never touches passwords, lock state, shell, login
class, or memberships. It replaces the earlier migration-local guarded
`Command("usermod", List("-d", …), Unless("sh", … awk … /etc/passwd))`
workaround; conf pins v0.15.0 and its `_gorum`, `_dserver` and `_gogios`
accounts use it (task s52, conf 7bc33b3), so that guarded command is gone. These homes live under `/var/run`,
which does not survive a reboot on OpenBSD; managing the passwd field does not
recreate the directory, so the ports keep their separate boot-time handling
(see the `/var/run` caveat in [user.md](user.md)).

### Templates (rich data + closures)

Rex templates embed Perl (loops over `@acme_hosts`/`@f3s_hosts`, per-server
`$hostname`, closures like `$ipv4address`, secret interpolation). gonf uses Go
`text/template`, so it cannot render those Perl templates as-is; recipes must
translate their logic to Go data and templates.

The Go-native replacement is **record-time content computation**: recipes are Go,
so anything the Perl template computed can be computed while recording — shared
arrays as package vars, per-host values via `ForHosts(key, func(host, v) {…})`
(or `MustHostValue` + `WhenHostname(host, …)`) fragments, secrets via `MustSecret` / `OptionalSecret`. The one-line `myname.tpl` becomes a Go
expression; zone-file loops become `EachKV`/`for` over the zone list emitting one
`InstallFile` per zone. This costs more lines than Rex but lives in one language
and is fully type-checked.

`WithTemplateData(any)` carries JSON-compatible maps, slices, and structs on
the file op. Templates render at destination apply with stable string helpers,
strict missing-key errors, and live destination facts under `.Gonf`. This covers
large config loops and per-host values while keeping closures in Go.

## The conf/gonf consumer

Every operational conf Rex task now has a Gonf owner (see the mapping below);
the consumer composes them with `RegisterMethods` + `WithCluster`, per-host
`WithValue` rows read through `ForHosts`, and explicit aggregates in
`gonf/tasks/tasks.go`:

| Prefix / cluster | Tasks | Notes |
|------------------|-------|-------|
| `frontends_*` / `frontends` (blowfish, fishfinger; doas; openbsd/amd64) | unattended upgrades (script, services, cron, newsyslog), base, myname, WireGuard hosts, uptimed, goprecords, rsync, gemtexter, ACME, httpd, inetd, relayd, PF, SMTPD, NSD, DNS failover, DTail, Gogios, Foostats, package repo, service accounts; by name only: acme_invoke, irc_bouncer, ping | `AggregateTasks("frontends", …)` with a registration-time membership check; `OptsPing` opts out of the struct-level `RequiresRoot` |
| `pis_netbsd_*` / `netbsd-pis` (pi0, pi1; doas; netbsd/arm64) | script, services, cron, newsyslog, vuln_audit_* | `WhenHostname(ClusterHosts())` fragments, per-host cron via `ForHosts` |
| `rocky_*` / `rocky-all` (pi2, pi3, r0–r2; sudo; linux) | gonf_link, packages, script, stamp_dir, units, logrotate; `rocky_kernel_audit_*` on the Pis | `SystemdTimer` per-host `OnCalendar` |
| `freebsd_*` / `freebsd-hosts` (f0–f3; doas; freebsd/amd64) | packages, script, services, stamp_dir, cron, newsyslog | hourly minute via `WithValue`; `@reboot` workaround |
| `rnodes_*` / `rocky-k3s` (r0–r2; root) | nfs_mount_monitor, persistent_journal | ports of `f3s/r-nodes/Rexfile` |
| `garage_*` / `garage` (f0–f2; doas) | config | configuration only, see below |
| `debian_pis_*` / `debian-pis` (pi2, pi3) | unattended upgrades and base | registered for explicit runs only, no aggregate yet |

### First-install prerequisites

The aggregates converge hosts that are already provisioned; they are not
first-host installers. Prerequisites a new host (or controller) needs:

- **Garage**: `garage_config` deploys `/usr/local/etc/garage.toml` and
  restarts Garage on a change. Installing the package, its `garage` group,
  `/var/db/garage` storage and the cluster layout are provisioning steps
  outside Gonf; the controller needs `gonf/secrets/garage/rpc_secret`
  (`just -f f3s/garage/Justfile init-secrets`).
- **Unattended upgrades**: cron jobs and timers call scripts installed by
  the `*_script` tasks, which on FreeBSD and Rocky need the `*_packages`
  interpreter (ksh); the task descriptions name these prerequisites, and the
  pattern aggregates record them in a working order.
- **Frontend certificates**: relayd and smtpd load keypairs from
  `/etc/ssl`, which exist only after `frontends_acme` plus one explicit
  `frontends_acme_invoke` (placeholders are copied from `foo.zone`), so a new
  frontend runs those before `frontends_relayd`/`frontends_smtpd`.
- **Controller inputs**: `gonf/secrets/frontends` (NSD TSIG key required,
  goprecords tokens optional) and the checkouts `~/git/shuriken.sh`
  (required by `frontends_gogios`) and `~/git/foostats` (optional, in-repo
  fallback); `GONF_SHURIKEN_ROOT` / `GONF_FOOSTATS_ROOT` override them and a
  missing plugin fails with the fix in the message (conf 63e83a8).

Operator-facing versions of these lists: conf `frontends/README.md` and
`f3s/garage/Justfile`.

Inventory invariants (from `cluster.go`): pi/r/f hostnames are substrings of the
live OS hostnames so `WhenHostname("piN")` / `WhenHostname("rN")` / `WhenHostname("fN")`
match; LAN hosts pin `WithSSHPort(22)` because `~/.ssh/config` maps
`*.buetow.org` to port 2.

## Implementation ordering

1. **Complete: `OnChange` on Service / Timer / Command / DaemonReload**
   (schema 11) — unblocks httpd, inetd, relayd, smtpd, nsd, gorum, pf, r-nodes
   monitor + journal, garage restart, and login.conf `cap_mkdb`.
2. **Complete: `MustSecret` / `OptionalSecret` + plan-secrecy doc note** — unblocks goprecords_upload,
   nsd key.conf, and `garage_config`.
3. **Complete: `WithEnv` on Package** — unblocks dtail_install, gogios_install,
   complements `pkgrepo_setup`.
4. **Complete: mechanical frontends tasks** (base pkgs, hosts_wg, uptimed,
   acme_invoke, pkgrepo_setup, foostats, ircbouncer, nsd_failover, gogios
   user/dirs/cron scaffolding); `cron_test`, `gorum_install` and `gorum` are
   excluded instead (see the mapping).
5. **Complete: template-heavy tasks** with record-time Go content or
   `WithTemplateData`: base/myname, gemtexter, acme, httpd, inetd, relayd,
   smtpd, nsd zones, gogios.json, pf.
6. **Complete: Garage and r-nodes ports** (`garage_config`,
   `rnodes_nfs_mount_monitor`, `rnodes_persistent_journal`).
7. **Nice-to-have** (only if consumers still feel the pain): cron `@reboot`.

Steps 1–3 are gonf-library work (tests + plan bump + docs); steps 4–6 are
conf-consumer work tagged to a gonf release; each port flips task ownership from
Rex to gonf (comment out the Rex task or delete it once the live deploy
converges).

Retirement status (2026-09-22): the mapping has no operational Rex task left
without a Gonf owner or an explicit exclusion, but the Rexfiles are not
retired. `conf/Rexfile`, `frontends/Rexfile`, `f3s/r-nodes/Rexfile` and
`playground/Rexfile` stay until authorized live rollout, a second
idempotent apply and failover checks have passed (conf task v42); the
frontends Rexfile is marked legacy and must not be run. dotfiles'
`pkg_fedora` still installs the `Rex` package for them. Removing the Rexfiles
and the Perl `.tpl` templates no Gonf recipe reads is the last step.

## Rex task mapping

Every conf Rex task and its gonf fate. "consumer" = already lives in
`~/git/conf/gonf`. Feature codes: [on-change], [secrets], [pkg-path], and
[templates] — see gaps above.

Privilege note: the frontends tasks write root-owned files (`/etc/*`,
`/usr/local/*`, `/root/.profile`) while the SSH login is `rex`, so those tasks
carry `RequiresRoot` / `Privileged()` exactly like the consumer's unattended
structs — `File` / `InstallFile` have no per-op elevate. The Garage port is
also task-privileged: it writes the final root-owned configuration directly,
so the plan has no login-owned `/tmp` secret staging step.

### frontends/Rexfile (target: gonf cluster `frontends`, tasks `frontends_*`)

| Rex task | gonf port | Status / needs |
|----------|-----------|----------------|
| `commons` (run_task aggregator) | `AggregateTasks("frontends", …, frontendSetupTasks()...)` | **Done** (consumer; task n52, conf c5ed357). The explicit list is still a superset of Rex `commons`' 18-task subset; `frontends_acme_invoke`, `frontends_irc_bouncer` and (owner decision 2026-09-22, conf 747d90b) the `frontends_ping` diagnostic are `Operational()` and listed as exclusions, and a registration-time check panics when a `frontends_*` task is in neither list, so a new task cannot silently fall out of setup runs |
| `id`, `dump_info` | — | **Excluded** (interactive diagnostics; `Command("id", nil)` ad hoc) |
| `base` (6× pkg present; `pkg_scripts="…"` append to `/etc/rc.conf.local` (znc added on the ircbouncer host); `touch /etc/rc.local`; `/etc/myname` from closure template; `tmux-edit-send` source file) | `frontends_base`: `Package` ×6, `/etc/rc.local` (0644 root:wheel), and the `pkg_scripts` line exactly as `rcctl` writes it (task i82); `frontends_myname`: `/etc/myname` from inventory via `ForHosts` | **Consumer** — tasks 44b2ee4, o52, i82. `tmux-edit-send` is **retired**, not ported: its source was removed in conf e4638ec, neither frontend has `/usr/local/bin/tmux-edit-send`, and the dangling Rex file block was dropped (task 462) |
| `hosts_wg` (append `etc/hosts.wg.append` lines, skip comments/blanks) | `frontends_wire_guard_hosts`: `File("/etc/hosts", WithLines(...))` from shared inventory | **Consumer** — task 44b2ee4; local plan verified, live rollout remains explicit |
| `uptimed` | `frontends_uptimed`: `Package("uptimed")` + `Service("uptimed")` | **Consumer** — task 44b2ee4; local plan verified, live rollout remains explicit |
| `goprecords_upload` (token from secrets → `/etc/goprecords-upload.token` 0600; script; `daily.local` append; old script absent; old daily.local line stripped) | `frontends_goprecords`: `Package("curl")` + optional controller secret + `File` + `InstallFile` + `File(WithLine)` + `WithoutLine` + `NoFile` | **Consumer** — task 44b2ee4; local plan verified, live rollout remains explicit |
| `rsync` (pkg; rsyncd.conf + rsync.sh templates; root crontab rebuilt via temp files + run) | `frontends_rsync`: `Package("rsync")` + 2× Go-computed template content + `Cron("frontend-rsync", WithCommand("-ns /usr/local/bin/rsync.sh"), WithLegacyCommand("-ns /usr/local/bin/rsync.sh"), WithMinute("*/5"))` | **Consumer** — task g52; the legacy root crontab line is adopted by exact command match (user defaults to root; OpenBSD `-ns` flags ride in the verbatim command field). Live rollout remains an explicit operator action |
| `gemtexter` (template → `/usr/local/bin/gemtexter.sh`; daily.local append) | `frontends_gemtexter`: `InstallFile` of the Perl-free script + `File(dailyLocal, WithLine)` | **Consumer** — task 44b2ee4; local plan verified, live rollout remains explicit |
| `acme` (2 templates over `@acme_hosts`; daily.local append) | `frontends_acme`: `acme-client.conf.tmpl` and `acme.sh.tmpl` with `WithTemplateData` over one certificate list (sites, standby twins, host FQDN; relayd's keypairs share it) + `File(dailyLocal, WithLine)` | **Consumer** — tasks o52, m52 (conf 44fcc88: skip/unchanged/changed/failed outcomes, reload only for changed material, no separate ipv4./ipv6. requests). Certificates are requested only by `frontends_acme_invoke` |
| `acme_invoke` (run acme.sh every deploy) | `frontends_acme_invoke`: `Command("/usr/local/bin/acme.sh", nil)` without guards (runs every explicit apply — matches Rex) | **Consumer** — task v42; intentionally excluded from `frontends` because certificate issuance is an operator action |
| `httpd` (rc.conf.local flags append; httpd.conf template **restart-on-change**; htdocs dirs; fallback page + health-check `index.txt` template; service) | `frontends_httpd` controller-renders each host config and installs it with core `WithValidation("httpd", List("-n", "-f", CandidatePath))` (task j52, conf 122731f), then change-gates the live restart | **Consumer** — task s42; local plan verified, live rollout remains an explicit operator action |
| `inetd` (flags append; login.conf.d/inetd; inetd.conf restart-on-change; service) | `frontends_inetd`: `File(WithLine)` + `InstallFile` + `LoginClass("inetd", …)` (task t52, conf 7ec3015) + `Service("inetd", WithRestart, OnChange(...))` | **Consumer** — task s42; local plan verified, live rollout remains an explicit operator action |
| `relayd` (flags append; login.conf.d/daemon + `cap_mkdb` on change; relayd.conf 0600 restart-on-change; service; daily.local append) | `frontends_relayd` controller-renders `relayd.conf`, validates it with core `WithValidation` (task j52), removes the former `login.conf.d/daemon` fragment with `NoLoginClass` (task t52; owner decision 2026-09-22, the manually edited `/etc/login.conf` daemon class with openfiles 4096 is authoritative and not managed by gonf; conf c3ccfde), then change-gates the restart | **Consumer** — task s42; local plan verified, live rollout remains an explicit operator action |
| `smtpd` (aliases → `newaliases` on change; virtualdomains/users; 3 reject lists; smtpd.conf restart-on-change; service) | `frontends_smtpd` publishes six lookup tables plus the host config as one core `ConfigSet` validated with `smtpd -n` before live writes (task i52, conf 9e7419d); `newaliases` watches aliases, while SMTPD restart fans in config/table changes | **Consumer** — task t42; local plan verified, live validation and rollout remain explicit operator actions |
| `nsd` (flags append; key.conf from secret; nsd.conf.master; per-zone templates; zone removals; restart-if-changed; service) | `frontends_nsd` reads `MustSecret(paths.FrontendSecret(...))`; on blowfish it installs immutable inputs for the `dns-publish.ksh` publisher, which owns zone validation, serials and reload; on the standby the key include and `nsd.conf` are one `ConfigSet` validated with `nsd-checkconf` in the chroot (task i52), then change-gates NSD restart | **Consumer** — task t42; local plan verified, live validation and rollout remain explicit operator actions |
| `nsd_failover` (script + root crontab via run) | `frontends_dns_failover`: installs the script and creates marker-managed `Cron("frontend-nsd-failover", WithCommand("-ns /usr/local/bin/dns-failover.ksh"), WithLegacyCommand(...), WithMinute("*"))`, which adopts the legacy unmarked line by exact command match | **Consumer** — tasks t42 and g52 (exact-command adoption replaced the earlier cleanup command); local plan verified. Live rollout remains an explicit operator action |
| `dtail_install` (remove stray binaries; `PKG_PATH=… pkg_add -u dtail ‖ pkg_add dtail`) | `frontends_d_tail` cleans only unpackaged legacy binaries and uses `Package("dtail", WithEnv(map[string]string{"PKG_PATH": …}), IsLatest)`; on an absent OpenBSD package `IsLatest` installs first, while installed packages use `pkg_add -u` | **Consumer** — task u42; local plan verified, live rollout remains an explicit operator action |
| `dtail` (dtail_install + adduser `_dserver` + `usermod -d` + daily.local appends + service) | `frontends_d_tail` includes the no-login account with `WithManageHome` existing-home convergence (task s52), both daily hooks, and `Service("dserver")` | **Consumer** — task u42; individual task remains independently applicable |
| `pkgrepo_setup` (`PKG_PATH` export appended to `/root/.profile`) | `frontends_pkg_repo` keeps the signed repository export in `/root/.profile`, byte for byte the quoted line Rex wrote (task hb2); package resources carry the same environment themselves | **Consumer** — tasks u42, hb2; no unsigned fallback. Adopting an unquoted or older-URL variant without a duplicate needs core `WithKeyedLine` (task r52, branch `r52-core`, unreleased) |
| `gogios_install` (uname branch: OpenBSD custom-repo `pkg_add -u ‖ install`; FreeBSD branch is dead code) | `frontends_gogios` uses `Package("gogios", WithEnv(map[string]string{"PKG_PATH": …}), IsLatest)` (frontends are OpenBSD-only); absent packages install before later latest updates | **Consumer** — task u42; local plan verified, live rollout remains explicit |
| `gogios` (pkg ×2; adduser `_gogios`; dirs; gogios.json template over 3 arrays; `check_shuriken_age` sourced from `~/git/shuriken.sh`; `_gogios` crontab from template; rc.local appends) | `frontends_gogios` creates `_gogios`, converges runtime/status directories, Go-renders `gogios.json`, installs the external plugin, adopts the three legacy unmarked Gogios cron commands by exact command match (`WithLegacyCommand`, task g52), and preserves boot-time runtime-directory setup | **Consumer** — task u42; Garage and virtual-hosted bucket checks retain task 642's expected HTTP 403 result |
| `cron_test` (Rex cron canary, `_gogios` user) | — | **Excluded** — non-operational `/bin/ls` canary; `frontends_ping` and recorded-plan checks supersede it |
| `gorum_install` (source file; Rexfile has malformed owner/group attrs — fix at port) | — | **Excluded** — Gorum is disabled in the Rex `commons` aggregate; port it only with explicit re-enablement |
| `gorum` (adduser `_gorum`; gorum.json + rc.d/gorum restart-on-change; `/var/run/gorum`; service) | — | **Excluded** — disabled operationally; the account-only compatibility task does not imply service enablement |
| `foostats` (copies scripts from `~/git/foostats`; installs; dirs; daily.local; 5× p5-* pkg; newsyslog.conf) | `frontends_foostats` reads the canonical controller checkout, then installs reporting dirs, daily hook, Perl dependencies, and the full `newsyslog.conf` | **Consumer** — task u42; local plan verified, live rollout remains explicit |
| `ircbouncer` (pkg znc; service; fishfinger only) | `frontends_irc_bouncer`: `Package("znc")` + `Service("znc")` with `WhenHostname("fishfinger")` | **Consumer** — task v42; intentionally separate from `frontends` because Rex kept it outside `commons` |
| `pf` (pf.conf restart-on-change → `pfctl -f`; `/var/node_exporter` dir; exporter script; root cron (`-ns`); `rcctl set node_exporter flags`; restart) | `frontends_pf` validates a private candidate with core `WithValidation("pfctl", …)` before it replaces `/etc/pf.conf` (task j52), then reloads on change; it also converges exporter storage, script, cron and service flags | **Consumer** — task s42; local plan verified, live rollout remains an explicit operator action |

### f3s/garage (Rexfile retired; gonf cluster `garage` on f0–f2)

| Rex task | gonf port | Status / needs |
|----------|-----------|----------------|
| `garage_deploy` (secret substitution into per-node TOML; `/tmp` staging as `paul`; `doas install root:garage 0640`; unconditional restart; per-group auth, parallelism 1) | `garage_config`: `Host("f0"…"f2", WithSSHUser("paul"), WithSSHPort(22), WithPrivilege(PrivilegeDoas), WithGOOS("freebsd"))` cluster `.Parallel(1)`; per-host `garage.rpc_public_addr` inventory value; `MustSecret("garage/rpc_secret")` before plan recording; a shared `garage.toml.tmpl` receives structured `WithTemplateData`; `RequiresRoot` writes `/usr/local/etc/garage.toml` directly as root:garage 0640 and `Service("garage", WithRestart, OnChange(config))` restarts only after a config change | **Done** — no destination `/tmp` secret staging. `just init-secrets` copies a Rex-era controller secret once so migration cannot rotate the live cluster secret. |

### f3s/r-nodes/Rexfile (target: gonf cluster `rocky-k3s` — r0–r2 already registered)

| Rex task | gonf port | Status / needs |
|----------|-----------|----------------|
| `nfs_mount_monitor` (9 files + 5 dirs, root; change flag → one `daemon-reload` + timer restart; enable+start 3 units) | `rnodes_nfs_mount_monitor`: dirs, source files, change-gated reload/timer restart, shutdown-marker and drain services | **Consumer** — task f5525f5; local plan verified, live rollout remains explicit |
| `persistent_journal` (journald drop-in; `/var/log/journal` 2755; change → tmpfiles + restart journald; `journalctl --flush`) | `rnodes_persistent_journal`: dirs, drop-in, change-gated tmpfiles/journald restart and flush | **Consumer** — task f5525f5; local plan verified, live rollout remains explicit |

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
  v0.15.0, plan schema 21) — no "fleet needs transport" or
  missing-feature claims survive.
- All Rexfiles (four tracked, plus the retired `f3s/garage` one) are
  inventoried and every task appears exactly once in the
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
- `f3s/garage/Justfile` `deploy` calls
  `./gonf.sh cluster garage garage_config`; run `just -f f3s/garage/Justfile
  init-secrets` once to copy a Rex-era controller secret or initialize a new
  cluster before its first deployment.
- Feature docs: [README.md](README.md) (index), [plan.md](plan.md) (transport +
  privilege split), [package.md](package.md), [service.md](service.md),
  [cron.md](cron.md), [helpers.md](helpers.md), [options.md](options.md).
