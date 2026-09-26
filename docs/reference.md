# gonf quick reference

Every user-facing feature in one place, current as of v0.24.0 (plan schema
27). Background, rationale and history live in [design/](design/README.md).
New to gonf? Start with the [tutorial](tutorial/README.md).

## Concepts

| Term | Meaning |
|------|---------|
| recipe | A Go program that registers tasks and calls `cli.CLI()` from `main`. |
| task | A named function whose body declares resources. Options on the task say where and as whom it applies. |
| controller | The host running the recipe binary. Task bodies, secrets and opaque predicates run here. |
| destination | The host that applies the plan. Serializable guards, templates and `${HOME}` resolve here. |
| plan | Versioned JSONL of resource ops, plus a blob store for large files and synced trees. |
| apply | Interpreting a plan. There is one engine: local runs, `gonf apply`, push, cluster and fleet all go through it. |

```text
gonf <task>...                 record -> apply locally (temp plan dir, removed afterwards)
gonf plan -o dir <task>...     record -> dir/plan.jsonl + dir/blobs/ (or dir/plan.age)
gonf apply dir/plan.jsonl      decode -> apply, on any host with gonf
gonf push user@host <task>...  record in memory -> ssh -> gonf apply -
```

Recording runs task bodies once and emits ops. Go control flow in a body
(`if hostname == ...`) runs on the controller and never reaches the plan; use
the `When*` helpers for anything the destination must decide.

## Minimal recipe

```go
package main

import (
    . "github.com/snonux/gonf/api"
    "github.com/snonux/gonf/cli"
)

func main() {
    Task("hello", "Write ~/.hello", func() {
        File(Home(".hello"), WithContent("hi\n"), WithMode(0o644))
    })
    Task("motd", "Install /etc/motd", func() {
        File("/etc/motd", WithContent("welcome\n"), WithMode(0o644))
    }, Privileged()) // gonf -privilege=sudo motd
    cli.Main()
}
```

`api` re-exports every resource option (`resource/options`), the inventory
(package `inventory`), `Refuse` and `Dependency`, so one dot import is the
intended style. The older `api/options` package still re-exports the options
for qualified use; do not dot-import it next to `api` (Go rejects the
duplicate names).

## Tasks

### Registering

| API | Effect |
|-----|--------|
| `Task(name, desc, fn, opts...)` | Register a task. |
| `RegisterMethods(v, opts...)` | Register every exported method of struct `v` as a task. |
| `Aggregate(name, desc, regex)` | Task that records every activated task matching `regex`, sorted by name. |
| `AggregateTasks(name, desc, members...)` | Task that records the listed tasks in the listed order. |
| `Alias(name, desc, target)` | Second public name for `target`. Empty `desc` lists as `alias of <target>`. |
| `Run(names...)` / `RunContext(ctx, names...)` | Record and apply, same as `gonf <task>`. Inside a body, appends the other task's ops to the current plan. |
| `Tasks()`, `Matching(regex)` | List activated tasks (`TaskInfo`), or names matching a regex. |

### Task options

| Option | Meaning |
|--------|---------|
| `WhenLinux()`, `WhenDarwin()`, `WhenFreeBSD()`, `WhenOpenBSD()`, `WhenNetBSD()` | Destination guard: `goos` is that OS. |
| `WhenBSD()` | Destination guard: `goos` is `freebsd`, `openbsd` or `netbsd`. |
| `WhenOS(goos...)` | Destination guard: `goos` is one of the names, e.g. `WhenOS("linux", "darwin")`. Names other than `linux`, `darwin`, `freebsd`, `openbsd`, `netbsd`, or none at all, are a declaration error. |
| `WhenProfile(p...)` | Destination guard: `Facts.Profile` is one of `p`. `WhenProfile()` with no profile never matches, and naming such a task fails the record. |
| `WhenHostnameContains(s)` | Destination guard: hostname contains `s`. |
| `When(func(Facts) bool)` | Opaque predicate, evaluated on the controller only. Cannot travel in a plan. |
| `Privileged()` | Ops from this task apply as root (see [Privilege](#privilege)). |
| `Unprivileged()` | Clears `Privileged()`: the opt-out for one method of a `RequiresRoot` struct. |
| `Operational()` | Explicit action (cert request, one-shot, diagnostic). Never joins a pattern `Aggregate`. |
| `WithTaskCluster(name)` | Bind the task to a cluster for `ClusterHosts` / `ForHosts` / `EachHost`. |
| `Needs(tasks...)` | Record these tasks right before this one (see [Needs](#needs)). |

An `OptsX` companion adds to the struct defaults (`RequiresRoot`, `Opts()`)
for that method, so `TaskOptions{Needs("script")}` on a `RequiresRoot`
struct stays privileged. Use `Unprivileged()` to opt one method out:
`TaskOptions{Unprivileged()}`. (Before v0.22.0 an `OptsX` replaced the
defaults and an empty list opted out.)

`TaskOptions` is an alias for `[]TaskOption`.

### Where guards are evaluated

Since v0.19.0 serializable guards are always evaluated on the destination.

| Guards on the task | Controller (`-list`, aggregates) | Destination |
|---|---|---|
| none | active | always applies |
| serializable only | active; `-list` adds `[destination-guarded: <guard>]` when it does not hold here | `when_begin` decides |
| opaque only | active only if the predicate holds here | nothing travels; push, cluster and fleet refuse the plan |
| mixed | active only if the opaque part holds here | the serializable part decides |

A local run (`gonf <task>`, `Run`) resolves an aggregate member's serializable
guard against this host at record time and skips members that do not match.
`gonf plan` and every push record those members inside their `when_begin`.

To replace an opaque predicate on a pushed task, use a serializable guard or
move the check into the body as `OnlyIf`/`Unless` on a `Command`.

### RegisterMethods

```go
package home

type HomeTasks struct{}

func (HomeTasks) DescHelix() string      { return "Install helix" }
func (HomeTasks) Helix()                 { Package("helix") }
func (HomeTasks) OptsHelix() TaskOptions { return TaskOptions{Privileged()} }
func (HomeTasks) WhenHelix() TaskOption  { return WhenLinux() } // serializable

RegisterMethods(home.HomeTasks{}) // registers home_helix
```

| Companion / option | Meaning |
|--------------------|---------|
| `DescFoo() string` | Description for `-list`. Or let `gonf-desc` write it from the doc comment (below). |
| `OptsFoo() TaskOptions` | Per-method options, added after the struct default. `Unprivileged()` opts out of its `Privileged()`. |
| `WhenFoo() TaskOption` | Per-method guard such as `WhenLinux()`. A serializable guard travels in the plan, so the task still pushes. |
| `WhenFoo(Facts) bool` | Per-method opaque filter, controller only: push, cluster and fleet refuse the task. |
| `Opts() TaskOptions` | Struct-level default options. A method named `Opts` is never a task. |
| embedded `StructOption` | Same as `Opts()`, declared by embedding. `RequiresRoot` ships with gonf (`type T struct{ RequiresRoot }`). |
| `WithPrefix(p)` | Prefix for every task name, replacing the default below. `WithPrefix("")` registers bare method names. |
| `WithGroupWhen(opts...)` | Options applied to every method, before the struct default. |
| any `TaskOption` | Same as `WithGroupWhen(opt)`: `RegisterMethods(pkg.Pkg{}, WhenProfile("fedora"))`. |
| `WithCluster(name)` | Bind every method to a cluster. |
| `OnCluster(name)` | `WithCluster(name)` plus a destination guard: hostname contains one of the cluster's host names. |

Without `WithPrefix`, the prefix is `DefaultPrefix(v)`: the struct's package
and type name in snake_case, a trailing `Tasks` dropped, a type named like
its package and package `main` omitted. So `freebsd.Unattended` registers
`freebsd_unattended_*`, `home.HomeTasks` registers `home_*` and `main.Backup`
registers `backup_*`. `WireGuard` counts as one word (`rnodes.WireGuard`
registers `rnodes_wireguard_*`). (Under the dot import a type cannot be named `Tasks`
or `Home`: those are api functions.) Pass
`WithPrefix` when several structs share one namespace, such as
`WithPrefix("frontends_")` on `frontends.Web` and `openbsd.Unattended`.

`RegisterOnCluster(name, structs...)` is `RegisterMethods(v, OnCluster(name))`
for each struct, each under its own default prefix. A `RegisterOption` or
`TaskOption` in the list applies to all of them; `WithPrefix` is refused
(use `RegisterMethods` for a shared prefix):

```go
RegisterOnCluster(cluster.NameFreeBSD,
    freebsd.Carp{}, freebsd.NFS{}, freebsd.Relayd{}, freebsd.Zrepl{})
```

A task that applies to only part of its cluster narrows in its `WhenX`
companion with `WhenHostnameIn(hosts...)` (hostname contains any of them,
`Eq` for one, `In` for several), which adds to the `OnCluster` guard:

```go
func (Carp) WhenFailbackCron() TaskOption { return WhenHostnameIn("f0") }
```

Descriptions from doc comments: add one line per recipe package and run
`go generate ./...`:

```go
//go:generate go run github.com/snonux/gonf/cmd/gonf-desc

// StampDir ensures the /var/lib/unattended-upgrade stamp directory.
func (Unattended) StampDir() { EnsureDir("/var/lib/unattended-upgrade", RootPrivate) }
```

`gonf-desc` writes `desc_gen.go` with a `DescX` for every task method that
has a doc comment and no hand-written `DescX`: the first sentence, method
name and final period dropped, first letter upper case ("Ensures the
/var/lib/unattended-upgrade stamp directory"). A hand-written `DescX` wins.
`gonf-desc -check` exits 1 when the file is stale.

A companion with the wrong signature is a declaration error and that method
is not registered. Name methods for the action (`Unattended.Script`, not
`Unattended.UnattendedScript`).

`OnCluster` replaces the `WhenHostname(ClusterHosts(), func() { ... })`
wrapper in every body:

```go
RegisterMethods(Unattended{}, WithPrefix("freebsd_"), OnCluster("freebsd"))
```

- Same match as the wrapper: case-insensitive substring, any member.
- Recorded as one task-level `when_begin` (`hostname_contains`, `In` over
  the members, `Eq` for one host), not one fragment per host. The body runs
  once; `ForHosts`/`EachHost` fragments nest inside it.
- Off-cluster, `-list` shows `[destination-guarded: hostname_contains=f0|f1]`
  and a local run's pattern aggregate skips the task.
- The cluster must be registered before `RegisterMethods`. An unknown
  cluster, or a `WithCluster` naming another one, is a declaration error
  and registers nothing.

### Aggregates and aliases

`AggregatePrefix("freebsd")` is `Aggregate("freebsd", "Run all freebsd_*
tasks", "^freebsd_")`; a second argument replaces the description.

- Within one aggregate tree (the aggregate, nested aggregates and aliases
  they list) a task is recorded once, at its first position. A plain task
  body calling `Run("inner")` starts a new tree.
- A member still being recorded is a cycle and fails the record.
- `Aggregate` never picks up itself, an `Operational()` task, an alias of
  one, or an `AggregateTasks` containing one at any depth. `-verbose` names
  what it dropped. Bodies are not inspected: a plain task that calls
  `Run("op")` records `op` wherever it is recorded.
- `AggregateTasks` fails the record on an unknown member. An empty list, an
  empty or duplicate member, or the aggregate listing itself is a
  declaration error. Prefer it over a regex when membership is a safety
  decision.
- An alias records exactly the target's ops, guards, privilege and cluster,
  and has no options of its own. The target may be registered later; an
  unknown target or an alias of an alias fails the record.
- Errors carry the chain: `aggregate outer: aggregate inner: ...`.
- A nested `Run` failure fails the enclosing record even if the body handles
  the returned error. Decide optional work before calling `Run`.

### Needs

```go
Task("web", "", web, Needs("pf", "base"))
Run("web")         // pf, base, web
Run("base", "web") // base, pf, web
```

| Rule | Behaviour |
|------|-----------|
| Resolution | Under `RegisterMethods(..., WithPrefix("fe_"))`, `"pf"` tries `fe_pf`, then `pf`. Elsewhere a full name. Aliases resolve to their target. Resolved at record time. |
| Method expressions | `Needs(Unattended.Script, (*T).Method)` names the task `RegisterMethods` gave that method, whatever its prefix: jump-to-definition and rename work, a typo does not compile. A struct registered twice resolves within the dependent's prefix, else the need is ambiguous and fails the record. |
| Order | Needs record before the task, in declaration order, their own needs first. |
| Guards | A need records with its own guards and privilege, outside the task's `when_begin`. |
| Dedupe | Once per `Run(...)` list or aggregate tree; a later explicit name a need already recorded is skipped. A body's own `Run` starts a new scope. |
| Operational | A task that needs `Operational()` work never joins a pattern `Aggregate`. |
| No `Needs` | Plan unchanged. |

An unknown need fails the record. An empty name, a self need or a cycle
(`a -> b -> a`, aliases followed) is a declaration error and the task is not
registered.

### Facts

```go
type Facts struct {
    Profile  string // fedora | rocky | darwin | os-release ID | "unknown"
    GOOS     string // linux | darwin | freebsd | openbsd | netbsd
    Hostname string
}
```

`DetectFacts()` fills them, the same way on the controller and on the
destination at apply. Profile: `darwin` on macOS; else `rocky` when the
hostname contains "rocky"; else from `/etc/os-release` `ID` (`fedora`;
`rocky`, `centos`, `rhel`, `almalinux` map to `rocky`; any other ID
verbatim); `unknown` without one (the BSDs). Override with
`-profile` or `SetProfileOverride`. `-profile` is not forwarded to pushed
destinations.

Predicate helpers for `When`: `ProfileIs(p...)`, `And(...)`, `Or(...)`.

## Body-level guards and helpers

| API | Plan form | Direct (non-recording) form |
|-----|-----------|-----------------------------|
| `WhenPathExists(path, fn)` | `when_begin{path_exists}` around `fn` | probes the local filesystem now |
| `WhenHostname(s \| List(...), fn)` | `when_begin{hostname_contains}` around `fn`; a list gives one block per entry | runs `fn` when the local hostname matches |
| `EnsureDir(path, opts...)` | `ensure_dir`: destination creates it only if missing | `Dir` only when the path is not already a directory |
| `EnsureFile(path, opts...)` | `ensure_file`: creates an empty regular file if missing; keeps existing bytes, converges explicit mode/owner/group | same |
| `LinkIfExists(path, target)` | `link_if_exists` | `Link` if `target` exists, else `NoLink` |
| `SymlinkMap(parent, name, target, ...)` | one `link_if_exists` per pair | same |
| `InstallFile(dst, src, opts...)` | `File(dst, WithSource(src))`, default mode `0640` | same |
| `SyncDir(dst, glob, opts...)` | `Dir(dst, WithSourceGlob(glob))`, dir `0700`, files `0640` | same |
| `GitGlobal(k, v, ...)` | one `git config --global` `Command` per pair | same |

Later options override the helper defaults. `SyncDir` installs each match by
basename only; no subdirectory tree.

Direct apply (no recording) needs resource IDs to be unique across every
`WhenHostname`/`WhenPathExists` fragment that matches the host; a collision
is a declaration error naming both conditions.

Other helpers:

| API | Meaning |
|-----|---------|
| `Home(elem...)` | `$HOME/elem...` of the controller, where the recipe runs. For sources. |
| `DestHome(elem...)` | `${HOME}/elem...`, expanded on the destination at apply. For targets. An element escaping the home (`..`) is a declaration error. |
| `Expand(p)` | Expand a leading `~` (controller home, like `Home`). |
| `List(a, b, ...)` | `[]string` for multi-path resources, `Command` args and key/value lists. |
| `${HOME}` in a path | Expanded on the destination at apply. Any other `${...}` is an apply error. |

The rule: `Home` = where the recipe runs (sources), `DestHome` = where the
plan applies (targets). `Home` records the controller's home literally, so a
push from `/home/paul` to a Mac would write into `/home/paul` there.

`${HOME}` expands in every destination path: the path of every file, dir,
link, sync and ensure op, `WithSymlink`/`WithHardlink` targets,
`LinkIfExists`/`SymlinkMap` targets, `WithDir` and `Creates` of a `Command`,
`WhenPathExists`, and `ConfigSet` member paths, `WithChroot` and
`WithStagingDir` (schema 26, declared only by such a set; older binaries
refuse it). It does not expand in argv (`Command` args, validators) or file
content. It is the applying process's `$HOME`, else its user database entry;
an empty or relative home fails the apply. In an elevated chunk (sudo/doas)
that is whatever home the elevation tool leaves, usually root's, so keep
`DestHome` targets in unprivileged tasks. Controller-side sources
(`WithSource`, `WithSourceGlob`, `WithSourceBase`, `InstallFile`/`SyncDir`
sources) and `WithHome` refuse the token as a declaration error.
| `EachKV(list, fn)` | Call `fn(k, v)` per pair. Odd length is a declaration error and calls nothing. |
| `ParseKV(list)` | Same, returning `[][2]string, error`. |
| `RenderTemplate(path, data)` | Render a template on the controller (see [Templates](#templates)). |

## Resources

Constructors return a `Resource` (pass it to `DependsOn`/`OnChange`).
Constructors with a `Path` type parameter accept a `string` or `List(...)`;
`Files`, `Dirs`, `Links`, `Services`, `Timers` take a `[]string`,
`Packages` takes names (`Packages("git", "tmux")`). Options are typed per
resource family, so an unsupported option is a compile error. An invalid combination is a declaration error and the
resource is not registered.

### Shared options

| Option | Applies to | Meaning |
|--------|------------|---------|
| `DependsOn(res...)` | all | Apply after these. A `Multi` expands to its members. |
| `IsAbsent`, `No*` constructors | File, Dir, Link, Package, Service, Timer, SystemdTimer, Cron, LoginClass | Ensure it is gone. |
| `WithSensitive` | File, Dir/SyncDir, ConfigSet and `ConfigFile`, Command (needs `WithName`), Package, Cron, SystemdTimer | Mark the op as secret-bearing (see [Secrets](#secrets)). Other kinds refuse it at compile time. |
| `OnChange(res...)` | Command, Service, Timer, DaemonReload | Change gate plus ordering (see [Change gates](#change-gates)). |
| `WatchChanges(ids...)` | same | Gate on resource IDs, no ordering. |
| `Perm(mode, owner)` | File, Dir and every wrapper taking their options (`EnsureFile`, `InstallFile`, `SecretFile`, `EnsureDir`, `SyncDir`, `ConfigFile`) | `WithMode` + `WithOwner` + `WithGroup` in one: `owner` is `"user:group"`, `"user"` or `":group"`. Records the exact same plan. A malformed owner is a declaration error. |
| `Root` | owner spec for `Perm` and `WithOwner` | root and the destination's root group (root on Linux, wheel on the BSDs and macOS). Recorded as group `0`, so the destination picks the name, never the controller. |
| `RootOwned`, `RootExec`, `RootPrivate` | same as `Perm` | `Perm(0o644, Root)` / `Perm(0o755, Root)` / `Perm(0o600, Root)` on a file; on a directory `RootOwned` and `RootExec` are `0o755`, `RootPrivate` is `0o700`. Record exactly the `Perm` plan. |

Parent directories are ordered for you. A resource creating something
inside a directory the same plan creates (`Dir`, `SyncDir`, `EnsureDir`)
applies after it, as if it had `DependsOn` on it:

```go
EnsureDir("/usr/local/sbin", WithMode(0o755))
InstallFile("/usr/local/sbin/x", src, WithMode(0o755)) // no DependsOn needed
```

- Only the nearest declared ancestor counts; nested dirs chain.
- Paths compare as written (cleaned, symlinks not resolved). A `Link` is
  never a parent.
- Absent resources get no edge. A present resource inside an absent
  directory is a conflict gonf neither orders nor refuses.
- Nothing moves across a `when_*` block or a recorded privilege chunk;
  `api.Apply` orders across its own chunks.
- An inferred edge that would close a cycle with your `DependsOn` is
  dropped. An explicit `DependsOn` next to it is harmless.
- Done at apply time, so plan files do not change. An older destination
  gonf applies in declared order.

`options.Option` (`func(any)`) is the untyped legacy form. Convert a stored
`[]options.Option` with `ToFileOptions`, `ToDirOptions`, and so on; the
adapter cannot check the family.

### File

```go
File("/etc/motd", WithContent("hello\n"), WithMode(0o644))
File("/etc/app.conf", WithSource("assets/app.conf.tmpl"), WithTemplateData(cfg))
File("/etc/lines.conf", WithLines("a=1", "b=2"), WithoutLines("stale"))
File("/root/.profile", WithKeyedLine("export PKG_PATH=", `export PKG_PATH="https://repo/"`),
    Perm(0o644, Root))
File("/etc/hosts", WithBlock("fleet", "10.0.0.1 a", "10.0.0.2 b"), Perm(0o644, Root))
File("/etc/httpd.conf", WithContent(conf), WithValidation("httpd", List("-n", "-f", CandidatePath)))
NoFile("/tmp/old.txt")
```

| Option | Meaning |
|--------|---------|
| `WithContent(s)` | Inline content. |
| `WithContentFrom(s, err)` | `WithContent` for a render that can fail: `WithContentFrom(render(data))`. A non-nil `err` refuses the file (declaration error). |
| `WithSource(path)` | Copy a controller file. A `.tmpl` suffix renders it on the destination. |
| `WithTemplate` | Force template rendering without a `.tmpl` suffix. |
| `WithTemplateData(v)` | JSON-compatible data for the destination template; implies rendering. |
| `WithLines(l...)` / `WithoutLines(l...)` | Ensure / remove exact lines. `WithLine`/`WithoutLine` are singular forms. |
| `WithKeyedLine(key, line)` | Own the line starting with `key` (below). |
| `WithShellVar(key, value)` | `WithKeyedLine(key+"=", key+"=\"value\"")` for rc.conf-style files, with `\ " $` and backquote escaped in the value. `key` must be a shell name (dots allowed, for loader.conf). |
| `WithBlock(name, lines...)` | Own the lines between `# BEGIN GONF <name>` and `# END GONF <name>` (below). |
| `WithValidation(bin, args)` | Validate a candidate before publishing (below). |
| `WithMode(m)` | Mode. Setuid/setgid/sticky accepted as raw octal or `os.Mode*` flags. Bits above `0o7777` are refused. |
| `WithOwner(u)` / `WithGroup(g)` | Owner name; group name or numeric gid. Only explicitly set ownership is recorded. `WithOwner("u:g")` and `WithOwner(Root)` set both, like `Perm`. |
| `WithName(n)` | ID becomes `File[n]` while still managing the path. Needed for several declarations on one file. |
| `WithParam(v)` | Override `{{.Param}}`. |

Sharp edges:

- Mode defaults to `0640` and is applied even when `WithMode` is not given.
  A bare line edit on a `0644` file makes it `0640`. Pass mode, owner and
  group explicitly on shared files.
- Line edits cannot combine with `WithContent`, `WithSource`,
  `WithValidation`, `EnsureFile` or `SecretFile`.
- Edit order: blocks, then `WithoutLines`, then keyed lines, then `WithLines`.
- Any line edit rewrites the file with its dominant line ending (CRLF only
  if CRLF lines outnumber LF lines). A new file gets `\n`.
- Content up to 512 KiB travels inline (`content_b64`); larger content goes
  to a blob.

Keyed lines:

- The first line starting with `key` (after stripping that line's leading
  spaces and tabs) is replaced in place, unindented. Further matches are
  removed. No match appends `line`.
- `key` is a literal prefix, case-sensitive, no internal whitespace folding.
- Rules checked at declaration: `line` starts with `key` and has no line
  break; `key` does not start with whitespace; no key is a prefix of
  another key on the same File; no `WithLine`/`WithoutLine` starts with a
  key.
- A too-broad key (`"export "`) deletes unrelated lines. Drops log at Warn,
  replacements at Info. Include the delimiter: `"export PKG_PATH="`.
- Two File declarations keying one setting differently fight; keep one owner.

Managed blocks (`WithBlock`, plan schema 27, declared only by such a plan):

- gonf owns the lines between `# BEGIN GONF <name>` and `# END GONF <name>`
  and replaces them with `lines`. Lines outside the markers are never
  touched, so another tool can keep writing its own entries into the file.
- No markers: the block is appended with its markers (a missing file is
  created holding the block). No `lines`: the markers stay, the region is
  emptied.
- Markers match after trimming surrounding whitespace. A marker twice, only
  one of them, or END before BEGIN fails the apply without writing.
- Refused at declaration: an empty name, a name with surrounding whitespace
  or a line break, one name with two line sets, a block line with a line
  break or equal to a marker, a `WithLine`/`WithoutLine` equal to a block or
  marker line, and a keyed-line key that prefixes one.
- The markers are `#` comments; use it only on files that treat them so.
  Removing a block is not modelled: declare it empty, or edit the file.

Validation (`WithValidation`):

- Needs an absolute path and `WithContent` or `WithSource`. Put
  `CandidatePath` exactly once in `args`.
- gonf writes a `0600` candidate next to the target, runs the validator
  (argv, no shell, stdin `/dev/null`), and publishes by atomic rename only
  on exit 0. Failure leaves the live file untouched and reports no change,
  so `OnChange` gates do not fire.
- Runs on every non-dry-run reconciliation, including drift repair.
- Bounded by `-cmd-timeout` (default 5m). On timeout the validator and its
  descendants are killed. If gonf cannot signal it (EPERM under sudo/doas),
  it waits.
- Error output: combined stdout/stderr, lines joined by ` | `, first 4 KiB.
  For a sensitive op only the size is reported. Do not use a validator that
  echoes secrets.
- The parent directory must be owned by the applying uid, not
  world-writable, and group-writable only by your private group. Ancestors
  must be owned by root or you; writable ancestors need the sticky bit. No
  symlinks or `..`. On macOS and the BSDs every ancestor must also be
  readable by the applying user.
- Single file only. Use [ConfigSet](#configset) for files that must be
  validated together.

### Dir

```go
Dir("/var/lib/app", WithMode(0o755))
Dir("/opt/tree", WithSource("assets/tree"), WithPrune, WithFileMode(0o644))
Dir(Home(".config/app"), WithSourceGlob("assets/app/*"))
NoDir("/tmp/stale", WithPrune)
```

| Option | Meaning |
|--------|---------|
| `WithMode` | Directory mode, default `0750`. |
| `WithFileMode` | Mode for files copied from a source, default `0640`. |
| `WithOwner` / `WithGroup` | Ownership. |
| `WithSource(dir)` | Tree sync: mirror the tree, symlinks and empty dirs included. |
| `WithSourceGlob(pattern)` | Glob sync: install matching regular files by basename. Cannot combine with `WithSource`. |
| `WithPrune` | Remove unexpected entries (below); with `NoDir`, remove a non-empty directory. |
| `WithSourceBase(dir)` | Override the declared source directory used for `{{.Param}}`. |

Prune semantics:

- Tree sync removes every destination entry without a source counterpart,
  recursively. `x` is expected when the source has `x.tmpl`.
- Glob sync removes only non-matching regular files directly under the
  destination. Subdirectories and symlinks stay.
- Do not prune a directory that holds ConfigSet members or staging dirs:
  pruning deletes their pending markers and backups.

`.tmpl` entries in a synced tree render on the destination. FIFOs, sockets
and devices in a source tree fail the record.

### Link

```go
Link("/usr/local/bin/tool", WithSymlink("/opt/tool/bin/tool"))
Symlink("/usr/local/bin/tool", "/opt/tool/bin/tool") // the same
Link("/var/lib/app/data", WithHardlink("/data/app"))
NoLink("/tmp/stale-link")
```

- Replacing a real file or directory moves it aside to `path.old`, creates
  the link, then removes the aside. If the link fails, the original is
  restored.
- An existing `path.old` (even a dangling symlink) refuses the conversion.
  Remove it by hand.
- A non-empty directory aside cannot be removed: the link stays, the apply
  errors naming the backup, the next apply succeeds.
- Repointing an existing symlink never uses the aside.
- A symlink whose target does not exist is refused, so a typo fails the
  apply instead of leaving a dangling link. A dry run previews it as
  `would-change` instead, because an earlier resource may create the target.
- No owner or mode options.

### Command

```go
Command("touch", List("/tmp/marker"), Creates("/tmp/marker"), WithName("touch-marker"))
Command("true", nil, Unless("test", List("-f", "/tmp/skip")), OnlyIf("test", List("-d", "/opt/app")))
Command("newaliases", List(), OnChange(aliases))
Sh("systemctl restart 'my unit'", OnChange(unit))  // Command("systemctl", List("restart", "my unit"))
Noop("ping")                                      // changes nothing, reports ok
```

| Option | Meaning |
|--------|---------|
| `Creates(path)` | Skip when `path` exists (a relative `path` is resolved against `WithDir`). |
| `Unless(bin, args, ExpectExit(n), ExpectStdout(s))` | Skip when the guard succeeds: exit 0 by default, or the given exit code and trimmed stdout. |
| `OnlyIf(bin, args, ...)` | Run only when the guard succeeds. |
| `WithName(n)` | ID `Command[n]`. Without it the ID is the whole argv. |
| `WithDir(d)` | Working directory. |
| `WithEnv(map)` | Extra environment. |
| `WithElevate` | Run this one op as root inside an unprivileged task. |
| `OnChange(...)` | Run only after a watched change. |

Guards run on the destination. Prefer a first-class resource when one fits.

`Sh(line, opts...)` splits `line` into argv like a shell (`'...'`, `"..."`,
`\`) but runs no shell and expands nothing. The plan is the same as the
`Command` it spells, and so is the default ID (the words joined by spaces).
An unquoted `` | & ; < > ( ) $ ` * ? [ ``, a leading `#` or `~`, or `$`/`` ` ``
in double quotes is a declaration error. For real shell syntax use
`Command("sh", List("-c", ...))`.

`Noop(name)` registers `Noop[name]`: it runs nothing, always reports ok, and
can be a `DependsOn`/`OnChange` target. Use it instead of
`Command("true", nil, Unless("true", nil), WithName(name))`. It needs a
schema 25 destination.

### Package

```go
Package("rsync")
Package(List("git", "tmux"), IsLatest)
Packages("git", "tmux")                            // Package(List("git", "tmux"))
Package("dtail", WithEnv(map[string]string{"PKG_PATH": "https://repo/openbsd/"}))
NoPackage("oldpkg")
```

| OS | Backend |
|----|---------|
| Linux (Fedora, RHEL, Rocky) | `dnf` |
| OpenBSD | `pkg_add` / `pkg_delete` / `pkg_info` |
| FreeBSD | `pkg` |
| NetBSD | `pkgin` (+ `pkg_info`) |

`IsLatest` runs the upgrade path (`dnf update`, `pkg upgrade`, `pkg_add -u`,
`pkgin install`); a package that is not installed yet is installed instead
(`dnf install`, `pkg install`, `pkg_add`). `WithEnv` applies to probes and mutations. Needs root.
`Packages(names...)` takes no options; pass them via `Package(List(...), ...)`.

### Service and DaemonReload

```go
Service("httpd")                                  // started + enabled
Service("httpd", WithRestart, OnChange(conf))     // restart only when conf changed
Service("httpd", WithReload)
Service("foo", WithUser)                          // systemctl --user
Service("httpd", WithFlags(""), WithRestart)      // BSD rc flags
NoService("olddaemon")                            // stopped + disabled
```

| OS | Backend |
|----|---------|
| Linux | `systemctl` |
| OpenBSD | `rcctl` |
| FreeBSD | `service`(8) |
| NetBSD | `service`(8), enable via `/etc/rc.conf.d` |

| Option | Meaning |
|--------|---------|
| `WithRestart` | Restart once when already running. |
| `WithReload` | Reload once when already running. No restart fallback. |
| `WithUser` | systemd user bus. Refused on BSD backends. |
| `WithFlags(f)` | BSD startup flags: `rcctl set NAME flags f`, `sysrc NAME_flags=f`, NetBSD `NAME_flags='f'` in `/etc/rc.conf`. Refused on systemd at apply. |

`WithFlags` sets the flags after enable and before start, only when they
differ. A flags change counts as a change of the service: it fires
`WithRestart` even when `OnChange` holds. OpenBSD `WithFlags("")` writes the
`NAME_flags=` line `rcctl enable` writes for a base daemon, so it replaces
`File("/etc/rc.conf.local", WithLine("httpd_flags="))` + `OnChange(flags)`
without touching the host. Not for daemons whose rc.d script sets default
flags (e.g. nsd): empty never converges there. `WithRestart` without
`OnChange` restarts on every apply, so keep an `OnChange` on the daemon's
config. Needs a schema 25 destination.

`DaemonReload(opts...)` (Linux) runs `systemctl daemon-reload`:

```go
DaemonReload(WithUser, OnChange(units))            // preferred
DaemonReload(WithUser, DependsOn(units), IfChanged)  // legacy spelling, same op
DaemonReload(WithUser, DependsOn(units))           // always reload
```

- One reload per bus and recipe scope (`DaemonReload[system]`,
  `DaemonReload[user]`). A second declaration on the same bus in the same
  scope merges into the first; the merged reload is gated only if every
  declaration is gated.
- A merge is refused (declaration error `cannot merge`) across a when-block
  boundary, across a privilege change, or when it would make the reload
  depend on itself.
- A watched `Directory[p]` also fires on `File[p/...]` changes.
- `IfChanged` with neither `DependsOn` nor `WithWatch` ids can never fire;
  the plan pre-flight refuses it.
- `WithWatch(ids...)`: legacy watch ids for `IfChanged`; a later call
  replaces earlier ids.

### SystemdUnits

Composes unit files, one shared reload and the activations that need it:

```go
units := SyncDir(Home(".config/systemd/user"), "assets/systemd-user/*")
SystemdUnits(FanIn(units), ActivateTimer("backup", WithRestart), WithUserBus())
```

| Option | Meaning |
|--------|---------|
| `FanIn(res...)` | Inputs the reload watches. |
| `ActivateTimer(name, opts...)`, `ActivateTimers(names, ...)` | Timers to converge after the reload; restart gated on this composition's inputs. |
| `ActivateService(name, opts...)`, `ActivateServices(names, ...)` | Same for services. |
| `WithUserBus()` | User bus. |

Several compositions per scope share the bus's reload. A changed input
reloads once and restarts only units whose composition declared it. Keep
compositions in one block and privilege scope, and do not make one's inputs
depend on another.

### Timer

```go
Timer("fstrim")                          // enable + start fstrim.timer
Timer("backup", WithUser, WithEnableOnly)
NoTimer("oldjob")
```

`.timer` is appended when missing. Options: `WithUser`, `WithRestart`,
`WithEnableOnly` (enable/disable only), `OnChange`. `OnChange` never blocks
enable/start, it only holds the restart. Timer does not write unit files:
install them with `File`/`SyncDir`, reload with `DaemonReload`, or use
`SystemdUnits` or `SystemdTimer`.

### SystemdTimer

Writes a `.timer` and a oneshot `.service`, reloads when they change, then
enables and starts the timer. Linux only.

```go
SystemdTimer("backup",
    WithCommand("/usr/local/bin/backup.sh"),
    WithOnCalendar("*-*-* 03:00:00"),
    WithOnBootSec("10min"), WithPersistent,
    WithDescription("Nightly backup"), WithServiceDescription("Run backup once"),
    WithAfter("network-online.target"), WithWants("network-online.target"))
NoSystemdTimer("backup")   // stop, disable, remove both units
```

- `WithCommand` and `WithOnCalendar` are required.
- System units go to `/etc/systemd/system/`, `WithUser` units to
  `~/.config/systemd/user/`.
- `WithRestart`, `WithEnableOnly` pass through to activation.
- Declared after a same-bus `SystemdUnits`/`DaemonReload` in the same task,
  it shares that reload. It keeps its own reload when alone, on the other
  bus, declared earlier, in another block or privilege scope, or when its
  `WithAfter`/`WithWants` name a unit the composition may install. A later
  same-bus composition that could install such a unit is refused; declare
  the timer last.

### Cron

```go
Cron("backup",
    WithCommand("/usr/local/bin/backup.sh"),
    WithCronUser("root"), WithMinute("0"), WithHour("2"),
    WithCronEnv("PATH=/usr/bin:/bin"))
NoCron("backup", WithCronUser("root"))
CronAt("backup", "0 2 * * *", "/usr/local/bin/backup.sh", WithCronUser("root"))  // same op
```

| Option | Meaning |
|--------|---------|
| `WithCommand` | Job command, required unless absent. |
| `WithCronUser` | Crontab owner, default `root`. |
| `WithMinute`, `WithHour`, `WithMonthday`, `WithMonth`, `WithWeekday` | Schedule fields, default `*`. |
| `WithSchedule("0 2 * * *")` | All five fields. A bad schedule is a declaration error. |
| `WithCronEnv("K=V")` | Environment line above the job. |
| `WithLegacyCommand(cmd)` | Remove unmanaged entries with this exact command, any schedule, before adding the job. Not with `NoCron`. |

- The job lives between `# BEGIN GONF Cron[name]` and `# END GONF Cron[name]`.
- Five-field syntax: numbers, `*`, lists, ranges, `/step`, three-letter
  month and weekday names. `@` directives are refused.
- An unmanaged entry identical to the job (same five fields and exact
  command, in that user's crontab) is adopted by default, so it does not run
  twice: the block takes the entry's place, so the job keeps its
  environment. Not when the job has `WithCronEnv`. `WithLegacyCommand` is
  only needed for a different command or schedule.
- Own user: no `crontab -u`. Other users: `crontab -u USER`, needs root.
- An advisory lock covers read/merge/write per crontab, between gonf
  processes only.
- A failing `crontab` run reports exit code and output sizes, never the
  output. Run `crontab -l` by hand to see it.

### User

Additive only: never deletes accounts or groups, never removes memberships,
never changes an existing account's primary group, shell, class or system
flag, never sets a password.

```go
User("_svc", WithPrimaryGroup("_svc"), WithSupplementaryGroups("audio", "video"),
    WithHome("/var/svc"), WithCreateHome, WithShell("/sbin/nologin"))
```

| Option | Meaning |
|--------|---------|
| `WithPrimaryGroup(g)` | Primary group at creation. Created if missing. `WithGroup` is the old spelling. |
| `WithUserGroup(g)` / `WithSupplementaryGroups(g...)` | Add memberships (also to existing accounts). |
| `WithHome(h)` | Home at creation. Does not create the directory. |
| `WithCreateHome` | Create the home at creation. |
| `WithShell(s)` | Shell at creation. |
| `WithLoginClass(c)` | BSD login class at creation. `WithClass` is an alias. Refused on Linux. |
| `WithSystem` | Linux system account. Refused on the BSDs. |
| `WithManageHome` | Also rewrite an existing account's passwd home field to `WithHome`. |

- Backends: Linux shadow-utils (`useradd`/`usermod`/`groupadd` with GNU long
  options), OpenBSD and NetBSD `useradd`/`usermod`, FreeBSD `pw`.
- OpenBSD and NetBSD refuse more than 16 supplementary groups. FreeBSD
  refuses `WithCreateHome` with a relative or root `WithHome`.
- `WithManageHome` changes only the passwd field: no move, create or chown.
  `WithHome` must be absolute and clean, without `:`, newline or NUL. Pair
  it with a `Dir(home, WithOwner(...), DependsOn(account))`. Linux `usermod`
  may refuse while the account has processes.
- Requests no platform accepts (bad names, NUL bytes) fail `gonf plan`.
  Platform-specific refusals come from the destination, before any mutating
  command. One invalid `User` aborts a whole `api.Apply`.
- The created home's mode is platform-defined. Declare a `Dir` if it
  matters.

### LoginClass

OpenBSD only. Installs `/etc/login.conf.d/<class>`, removes a stale
`<class>.db`, and returns a handle for `OnChange`.

```go
class := LoginClass("inetd", "assets/etc/login.conf.d/inetd")
Service("inetd", WithRestart, OnChange(class))
NoLoginClass("inetd")
```

- Default `root:wheel 0644`. File options are appended; `WithContent` or
  `WithSource` replace `src`. Line edits are refused.
- Recorded inside an OpenBSD requirement block: any other GOOS refuses the
  whole plan, dry run included, before anything is written.
- Not allowed inside `WhenPathExists`. Allowed under host-fact guards.
- The class name must match `[A-Za-z0-9][A-Za-z0-9._-]*`, not end in `.db`,
  and appear as a record name in the content.
- No `cap_mkdb`, no `/etc/login.conf` edits, no accounts. Needs a
  destination gonf with plan schema 20 or later.
- The fragment replaces the whole same-named class, stock settings included.

### ConfigSet

Several files validated as one staged set before any live file changes. Each
member gets its own change handle.

```go
mail := ConfigSet("smtpd",
    ConfigFile("aliases", "/etc/mail/aliases", WithSource(aliases), WithMode(0o644)),
    ConfigFile("smtpd.conf", "/etc/mail/smtpd.conf", WithContent(conf), WithMode(0o644)),
    WithSetValidation("smtpd", List("-n", "-f", MemberPath("smtpd.conf"))))

Command("newaliases", List(), OnChange(mail.Member("aliases")))
Service("smtpd", WithRestart, OnChange(mail.Members("smtpd.conf")...))
```

| API | Meaning |
|-----|---------|
| `ConfigSet(name, opts...)` | Registers `ConfigSet[name]` plus `ConfigSetMember[name/key]` per member. |
| `ConfigFile(key, path, opts...)` | Member at an absolute, clean path. `WithContent` or `WithSource`, `WithMode` (default `0640`), `WithOwner`, `WithGroup`, `WithSensitive` only. |
| `WithSetValidation(bin, args)` | Validator, argv. At least one; all must exit 0. |
| `MemberPath(key)` | Member path placeholder: staged path while validating, live path when published. Use in content and validator args. |
| `MemberChrootPath(key)` | Same, relative to `WithChroot`. |
| `WithChroot(dir)` | Members and staging must be below `dir`. Validators are not chrooted. |
| `WithStagingDir(dir)` | Staging location, default the members' deepest common directory. |
| `set.Member(k)`, `set.Members(k...)` | Handles; `Members()` returns all. Unknown key fails the record. |

- Watch the set or its members. `OnChange(Dir(...))` never fires for a
  member.
- `.tmpl` sources are refused: render in the recipe and pass `WithContent`.
- Apply: lock member directories (`flock`, 5-minute wait), diff, stage the
  complete set as `0600` files in a private `.gonf-configset-<name>+XXXX`
  dir, validate, back up, publish member by member.
- Failed validation: nothing published, nothing reported, no gate fires.
- Failed publish: rolled back in reverse order. If a restore fails too, the
  error says `ROLLBACK INCOMPLETE` and the staging dir with `backups/` is
  kept.
- A crash between renames: the next apply revalidates, converges forward,
  and pending markers (`.gonf-pending.<hash>`) re-signal every member the
  interrupted apply published.
- Dry run skips staging and validation: it cannot prove the set is valid.
- Member directories on NFS are unsupported (no directory `flock`).
- Members are limited to 512 KiB each.

### SecretFile

```go
SecretFile("/etc/app.token", "app/token", WithMode(0o600), WithOwner("root"))
```

Writes exactly the resolved secret. Default mode `0600`. Content-changing
options, `.tmpl` paths, line edits and `IsAbsent` are refused.

### Change gates

| Resource | Effect of `OnChange` / `WatchChanges` |
|----------|---------------------------------------|
| Command | Runs only when a watched resource changed in this apply. |
| Service, Timer | State still converges; `WithRestart`/`WithReload` fires only on a watched change. |
| DaemonReload | Reloads only on a watched change since the bus's last reload in this apply. |

- `OnChange(res...)` also adds ordering dependencies; `WatchChanges(ids...)`
  does not.
- `OnChange()` with nothing to watch is a declaration error.
- Change reports are per privilege chunk. A watch across privilege classes
  is refused before anything applies.
- A metadata-only repair is not a change.

## Templates

| Where | How | Data available |
|-------|-----|----------------|
| destination | `.tmpl` source or path, `WithTemplate`, `WithTemplateData` | environment, `.Param`, `WithTemplateData` keys at the root and as `.Data`, `.Gonf.GOOS`, `.Gonf.Profile`, `.Gonf.Hostname` |
| controller | `RenderTemplate(path, data)`, result passed to `WithContent` | `data` keys at the root and as `.Data` only |

- Helpers: `join`, `lower`, `upper`, `trim`, `replace`.
- `missingkey=error`: an unknown key fails the render. A destination
  template that references `.Gonf`, env vars or `.Param` fails under
  `RenderTemplate`.
- `.Param` is the declared source path (for a synced tree: source dir +
  relative path), stable across runs.
- `.Gonf` facts are detected on the destination per apply chunk. Local
  applies honour `-profile`; pushed applies use the destination's own
  facts.
- `RenderTemplate` errors have resolved secret values redacted.

## Inventory

```go
web := Host("web", WithSSHUser("rex"), WithSSHHost("web.example"), WithSSHPort(2),
    WithPrivilege(PrivilegeDoas), WithValue("cron", [2]string{"6", "7"}))
edge := Cluster("edge", web, db).Parallel(2)
Fleet("homelab", edge, other)
```

| Host option | Meaning |
|-------------|---------|
| `WithSSHUser`, `WithSSHHost`, `WithSSHPort`, `WithSSHIdentity` | SSH connection. |
| `WithSSHDomain(d)` | SSH hostname defaults to `<name>.<d>`; an explicit `WithSSHHost` wins. |
| `WithHostnameMatch(f)` | The hostname fragment `OnCluster`, `EachHost` and `ForHosts` guard this host on. Default: the inventory name, so inventory names must be substrings of the hostnames unless this is set. |
| `WithPrivilege(PrivilegeNone \| PrivilegeSudo \| PrivilegeDoas)` | How elevated chunks are wrapped on this host. Default none. |
| `WithGOOS`, `WithGOARCH` | Cross-compile target for the remote binary. Default: `uname`. |
| `WithPlatform("goos/goarch")` | Both at once, e.g. `WithPlatform("freebsd/amd64")`. |
| `WithGonfPath(p)` | Remote install path, default `/usr/local/bin/gonf`. |
| `WithValue(key, v)` / `h.SetValue(key, v)` | Per-host data under a string key. |
| `WithData(v)` | Per-host data keyed by `v`'s concrete type. Use a struct type of your own. |
| `HostDefaults(opts...)` | Bundle options into one `HostOption` (see [Host defaults](#host-defaults)). |
| `WithPlanRecipient("age1pq...")` | Host's recipient for `plan -seal -for`. Validated at registration. |

- The inventory lives in package `inventory` (its own godoc page); `api`
  re-exports all of it.
- `Host`, `Cluster`, `Fleet` register themselves. Duplicates are declaration
  errors.
- Lookup: `LookupHost`/`MustHost`, `LookupCluster`/`MustCluster`,
  `LookupFleet`/`MustFleet`. `Hosts()`, `Clusters()`, `Fleets()` list them.
- `Cluster` takes `HostRef`s, each at most once. `.Parallel(n)` sets fan-out
  (default 5, `n < 1` means all at once).
- `Fleet` takes `ClusterRef`s. Hosts are deduplicated; each member cluster
  keeps its own `.Parallel(n)`.
- `MustHostValue[T](host, key)` reads a value; missing or wrong type is a
  declaration error.
- `HostData[T](host)` reads a `WithData` value; a missing value or an
  interface `T` is a declaration error.
- `ResetInventory()` clears the inventory.

### Host defaults

```go
freebsd := HostDefaults(WithSSHUser("paul"), WithPrivilege(PrivilegeDoas),
    WithPlatform("freebsd/amd64"), WithSSHDomain("lan"), WithData(Window{Hour: "3"}))
Host("f0", freebsd) // ssh f0.lan
Host("f1", freebsd, WithData(Window{Hour: "4"})) // replaces the default
```

- Options apply in order, bundles expanded in place: later wins.
- A `WithValue` key or `WithData` type a bundle set may be replaced by any
  later option. Set twice outside a bundle, or by a bundle after an explicit
  option, is still a declaration error: pass bundles first.
- Bundles nest.

### Per-host fragments

```go
RegisterMethods(Edge{}, WithPrefix("edge_"), WithCluster("edge"))

func (Edge) Cron() {
    ForHosts("cron", func(host string, w [2]string) {
        File("/etc/cron-window", WithContent(w[0]+" "+w[1]+"\n"))
        File("/etc/upload.token", WithContent(MustSecret("tokens/"+host)), WithMode(0o600))
    })
}
```

- `ForHosts[T](key, fn)` iterates the task's cluster in registration order
  and runs `fn` inside `WhenHostname(host, ...)`.
- `EachHost[T](func(v T))` does the same with each host's `WithData` value
  of type `T`; `EachHostNamed[T](func(host string, v T))` also passes the
  name. Everything below applies to both.
- `EachHostWith[T](func(v T))` is `EachHost` that skips a member without a
  `T` value instead of failing, for data only some hosts carry.
- Every member's value is type-checked before any fragment is recorded.
- Misuse (no cluster, empty key, nil `fn`, missing or mistyped value, an
  interface `T`) fails the record. A member without a value is an error,
  not a skip.
- `fn` runs only for hosts the entry point targets, so a single-host push
  reads only that host's secrets. Read per-host secrets inside `fn`.

| Entry point | Hosts `ForHosts` visits |
|-------------|-------------------------|
| `PushHost(h)`, `PreviewHost(h)` | `h` plus names that could match the same machine |
| `gonf push user@host` | exact inventory match on SSH host or name (user and port must not contradict); every member with raw ssh args or no exact match |
| `gonf cluster`, `gonf fleet` | the members, plus names that could match them |
| local `gonf <task>`, `Run` | names the local hostname contains |
| `gonf plan`, `RecordPlan` | every member |

"Could match the same machine" means aliases on the same SSH host and
compatible port (case-insensitive), plus any inventory name contained in
the machine's names (`pi1` for `pi10`), because the destination guard is a
substring test. Avoid host names that are substrings of each other when an
artifact must hold exactly one host's secrets. A machine registered under
two different SSH hosts (IP and DNS name) breaks the narrowing.

`ClusterHosts()` returns the task's cluster hosts for a manual loop; it
does not narrow.

## Privilege

| Knob | API | Meaning |
|------|-----|---------|
| needs root | `Task(..., Privileged())`, `RequiresRoot` embed, `WithElevate` on a Command | ops marked `elevate` |
| how | `-privilege=none\|sudo\|doas`, `SetPrivilege`, host `WithPrivilege` | wrap elevated chunks as `sudo -n gonf apply` or `doas gonf apply` |

- Tasks are unprivileged by default. Nothing is inferred from resource kind.
- A mixed plan splits into ordered chunks, one `gonf apply` each. Chunks are
  never reordered; a dependency on a later chunk is refused at record time.
- Local run with `-privilege=none`: as root, elevated chunks run
  in-process; as non-root, the run is refused before anything applies.
- Remote with `-privilege=none` and an elevated op: refused. Use sudo/doas,
  or drop `Privileged()` when the SSH login is root.
- `gonf push user@host` uses `-privilege`, not the inventory's
  `WithPrivilege`. `cluster` and `fleet` use each host's `WithPrivilege`.
- A local elevated chunk has a 10-minute timeout unless the context
  carries its own deadline.
- The elevated path requires the program to run through `cli.CLI()`.
- Fixed-argument sudoers/doas rules must allow `-cancel-pipe` (local
  re-exec) and `-relayed` (remote apply), plus `-cmd-timeout` and
  `-profile` when those are not at their defaults. A loose
  `gonf apply *`-style match on the tail is simpler.

## CLI

Exit codes: 0 success, 1 failure, 2 usage error. Every stderr line is
redacted against resolved secrets.

### Global flags

| Flag | Default | Meaning |
|------|---------|---------|
| `-list` | | Print activated tasks (`name<TAB>desc`), destination-guarded ones marked. |
| `-n`, `-dry-run` | off | Preview without changing anything. |
| `-verbose` | off | Debug logging. |
| `-quiet` | off | Warnings and errors only; the summary still prints. |
| `-profile p` | detected | Override `Facts.Profile`. Not forwarded to pushed destinations. |
| `-privilege m` | `none` | `none`, `sudo` or `doas` for local elevated chunks. |
| `-cmd-timeout d` | `5m` | Per backend command and validator. SIGTERM on expiry, SIGKILL 10s later. `0` or negative keeps the default. Non-default values are forwarded to the elevated re-exec and to remote gonf versions that accept the flag. |
| `-version` | | Release version. |
| `-plan-version` | | Plan schema this binary emits and applies (27). |
| `-strict-preview-version` | | Strict-preview capability (1). |
| `-sealed-version` | | Sealed-plan capability (1). |
| `-signed-version` | | Signed-envelope version (1). |

Settings changed by flags are scoped to one `cli.CLI()` call.

### Subcommands

| Command | Meaning |
|---------|---------|
| `gonf <task>...` | Record and apply locally. |
| `gonf plan [flags] <task>...` | Write a plan (below). |
| `gonf apply [flags] <plan.jsonl \| plan.age \| ->` | Apply a plan file or stdin. |
| `gonf push [-n\|-preview] [-id name] [-privilege m] [-- ssh-args...] user@host <task>...` | Record in memory, stream over ssh. `-id` default `push`. |
| `gonf cluster [-n\|-preview] [-j N] [-id name] [-host-timeout d] <cluster> <task>...` | Record once, push to every host in parallel. `-id` default `cluster-<name>`. |
| `gonf fleet [same flags] <fleet> <task>...` | Same over a fleet's unique hosts. `-id` default `fleet-<name>`. |
| `gonf hosts`, `gonf clusters`, `gonf fleets` | List the inventory. |
| `gonf plan-signer-keygen <file>` | New Ed25519 signer file (`0600`, never overwrites). Prints its trusted-signers line on stdout. |
| `gonf plan-verify [-trusted-signers f]... [-max-signed-age d] <file \| ->` | Verify a signed plan and write the bare `plan.age` to stdout. |
| `gonf dns-zone-serial <origin> <zone>` | Print the serial of the zone's single apex SOA. |
| `gonf dns-zone-equivalent <origin> <candidate> <committed>` | Exit 0 when both zones hold the same records ignoring the SOA serial and record order, 1 when they differ, 2 on error. |

### plan flags

| Flag | Meaning |
|------|---------|
| `-o dir` | Output directory, default `.`. Writes `plan.jsonl` (`0600`) and `blobs/` (`0700`). |
| `-id name` | Plan id in the header, default `plan`. |
| `-stdout` | JSONL to stdout. Refused for a plan with sensitive ops or blobs (use `-o`). |
| `-with-secrets` | With `-stdout`: print a sensitive plan anyway. |
| `-redacted` | Human preview to stdout, header op `plan_preview`, secrets `[redacted]`. Not applicable. Not with `-stdout`, `-with-secrets`, `-o`. |
| `-seal` | Write `dir/plan.age` (or sealed bytes to stdout with `-stdout`). |
| `-recipient r` | Extra `age1pq` recipient, repeatable. |
| `-recipients-file f` | Use this file instead of the default. Missing is an error. |
| `-no-default-recipients` | Ignore the default recipients file. |
| `-plaintext` | With `-o`: write plaintext even when sealing by default would apply. Not with `-seal`, `-stdout`, `-redacted`. |
| `-for host\|cluster\|fleet` | With `-seal`: one `dir/plan-<host>.age` per host. |
| `-sign f` | With `-seal`: sign every sealed artifact with signer file `f`. No default path. |

Output directory rules:

| `dir` | Result |
|-------|--------|
| missing | created, every new component `0700` |
| yours, not group/world-writable, or group-writable only by your private group (gid == uid, not 0) | used as is, mode unchanged |
| yours but not writable by you | refused before any task runs |
| world-writable (sticky `/tmp` too), group-writable by another group, owned by someone else, not a directory, symlink in the path | refused |

- Root has no private group: a root run refuses every group-writable dir.
- After any record error `dir` is unchanged. Blobs are staged in `$TMPDIR`
  and committed only after the whole record succeeded.
- A sensitive plan written in plaintext prints a warning naming the ops.
  If `dir` is in a git worktree that does not ignore `plan.jsonl` or
  `blobs/`, a second warning names them.

### apply flags

| Flag | Meaning |
|------|---------|
| `-n`, `-dry-run` | Preview. |
| `-identity f` | age identity, repeatable. Non-root default `${XDG_CONFIG_HOME:-$HOME/.config}/gonf/identity`. Root must pass it. |
| `-trusted-signers f` | Trusted-signers file, repeatable. Non-root default `.../gonf/trusted-signers`. Root must pass it. |
| `-require-signed` | Refuse anything that is not a signed plan. |
| `-max-signed-age d` | Freshness window, default `24h`. |
| `-strict-preview` | Dry run that rejects blobs (used by `push -preview`). |
| `-apply-dir`, `-cancel-pipe`, `-relayed` | Internal, set by gonf itself. |

- Input is sniffed: `GONF-SIGNED-PLAN/` = signed, `age-encryption.org/v1`
  = sealed, else a GONF-PUSH frame or bare JSONL.
- A plan file must be a regular file and not a symlink. Use `-` for pipes.
- `gonf apply` of a file runs every op in this one process and privilege;
  `elevate` is ignored. Run it under sudo/doas for privileged plans.

## Plans and remote push

### Plan format

```jsonl
{"op":"plan","version":21,"id":"demo"}
{"op":"when_begin","all":[{"fact":"goos","eq":"linux"}]}
{"op":"file","id":"File[${HOME}/.taskrc]","path":"${HOME}/.taskrc","mode":"0640","content_b64":"Li4u","has_content":true}
{"op":"when_end"}
{"op":"command","id":"Command[reload]","name":"reload","bin":"systemctl","args":["--user","daemon-reload"],"deps":["File[/etc/foo]"]}
```

- The header version is the lowest schema that can carry the plan. A plan
  without sensitive ops, keyed lines, pruning glob syncs, service flags or
  noops declares 21.
- A destination refuses a newer schema before any change.
- Ops apply in dependency order within each run between `when_*`
  boundaries, parent directories included (see [Shared options](#shared-options));
  ties keep recorded order. Nothing moves across a boundary.
- Failed `when_begin` predicates skip the block. A `require` block refuses
  the whole apply instead, before any change.
- Content up to 512 KiB is inline; larger files and synced trees are blobs
  under `blobs/`.

Schema versions (what an older destination refuses):

| v | Adds |
|---|------|
| 2 | `timer`, `daemon_reload` |
| 3 | `cron`, `service` |
| 4 | `owner`/`group` on filesystem ops |
| 5 | `deps` |
| 6 | `sync_dir.source_dir` |
| 7 | `systemd_timer` |
| 8 | `in` lists on `when_begin` predicates |
| 9 | `file.template`, `template_param` |
| 10 | `package.latest` |
| 11 | change gates on `command`, `service`, `timer` |
| 12 | `file.template_data`, `.Gonf` facts |
| 13 | `user` |
| 14 | `ensure_file`, `add_lines`/`remove_lines` |
| 15 | `package.env` |
| 16 | `file.name` identity |
| 17 | `cron.legacy_command` |
| 18 | `file` validators |
| 19 | `user.manage_home` |
| 20 | `when_begin.require` |
| 21 | `config_set`, `config_set_member` |
| 22 | `sensitive` (only declared when present) |
| 23 | `file.keyed_lines` (only when present) |
| 24 | `sync_dir.glob` (only for a pruning glob sync) |
| 25 | `service.flags`/`has_flags`, `noop` (only when present) |

### Pre-flight

`plan.ValidateChunks` refuses, before anything applies or any SSH traffic:

- a `DependsOn` or watch naming no recorded resource;
- a dependency on a later privilege chunk;
- a change watch across chunks;
- a `require` block nested under a non-host-fact condition.

It runs at record time, in `ApplyChunks`, before every push delivery, and in
`api.Apply`. Executing a single chunk (`gonf apply`, `ApplyPlan`,
`PushPayload`) does not re-run it.

### Push wire

- `GONF-PUSH/1`: magic, optional gzip+tar of blobs, gzip of the plan JSONL,
  streamed over ssh stdin to `gonf apply -`. Nothing is written to the
  controller's disk.
- The destination stages under `$TMPDIR/gonf-apply/<uid>/`, applies, and
  removes the run dir. Leftovers older than 24h are swept.
- A multi-chunk push with blobs uploads them once into a sticky dir owned by
  the SSH login user. It is removed after a successful push; after a failed
  one the next push of the same plan wipes it.
- `GONF-PUSH/2` adds a `key` line: sensitive blobs read by an elevated
  chunk are sealed to a per-push ephemeral `age1pq` key, uploaded as
  `sealed/<ref>.age`, and the key goes only to that chunk's stdin. The chunk
  decrypts into a private `sealed-run-*` dir and removes it afterwards.
  Needs a remote gonf 0.17.0 or newer; older remotes are refused before
  upload.
- The decompressed plan section is capped at 64 MiB
  (`plan.MaxDecompressedPushPlan`, raisable by an embedder).

### Remote binary sync

Before the first apply chunk, every push probes the remote gonf:

- Missing binary, older `-plan-version` or older `-version` than the
  controller: gonf cross-compiles `github.com/snonux/gonf/cmd/gonf`
  (`go build`, `CGO_ENABLED=0`, so the controller needs Go and the module),
  copies it with `scp`, and installs it at `WithGonfPath` using the host's
  privilege. Later chunks use that path.
- GOOS/GOARCH come from `WithGOOS`/`WithGOARCH`, else `uname -s`/`uname -m`.
  When `uname -m` names a port instead of a CPU (NetBSD `evbarm` on a
  Raspberry Pi), `uname -p` decides.
- One build per platform per run, in a private `$TMPDIR/gonf-cross-*` dir,
  re-verified (inode, owner, mode, SHA-256) before reuse. `$TMPDIR` and its
  ancestors must be owned by you or root and not writable by others
  without the sticky bit. In a rootless container, point `TMPDIR` at a
  directory you own.
- `push -n`, `cluster -n`, `fleet -n` still bootstrap the binary.
- `-preview` never builds or installs: the remote must already report a
  schema, strict-preview capability and release at least as new as the
  controller. It also rejects plans with blobs.
- `api.PushPayload` never installs; it needs a remote gonf 0.16.3 or newer.
- `-cmd-timeout` is forwarded only after probing that the remote binary
  accepts it, per privilege context. If the probe fails the chunk runs with
  the remote default and a warning.

### Timeouts and cancellation

| Knob | Bounds | Default |
|------|--------|---------|
| `-host-timeout` (cluster, fleet) | one host's whole push, all chunks | `10m`, `0` unlimited |
| `ConnectTimeout` in the ssh argv | TCP/SSH handshake only | 15s; an explicit `-o ConnectTimeout` in ssh args wins |
| `-cmd-timeout` | one backend command or validator | `5m` |
| elevated local chunk | one sudo/doas re-exec | `10m` |

- No overall timeout on a remote apply.
- A failing host cancels in-flight and not-yet-started hosts across the
  whole fleet; the error reports the abort once. A host killed by its own
  timeout says `(host timeout after ...)`.
- SIGINT, SIGTERM and SIGHUP (unless ignored, so `nohup` works) cancel a
  run: the running command gets SIGTERM, then SIGKILL after 10s, and no
  further op starts. A second signal force-exits the outer gonf, skipping
  cleanup. A `gonf apply` process ignores signals after the first.
- Validators are not stopped by a signal, only by `-cmd-timeout`; their
  result is discarded after an interrupt.
- Cancelling a push kills only the local ssh. The remote apply runs to
  completion.

## Secrets

### Resolving

| API | Behaviour |
|-----|-----------|
| `MustSecret(ref) string` | Required, non-empty. Any failure fails the record. |
| `OptionalSecret(ref) (string, bool)` | `("", false)` only for not-found. Every other failure fails the record. |
| `ResolveSecret(ctx, ref) ([]byte, error)` | Returns the typed error instead. Works outside recording. |
| `SecretFile(path, ref, opts...)` | File that is exactly one secret. |
| `SetSecretProvider(p)` | Install a provider. Once, in `main`, before any secret is resolved. |

Secrets resolve on the controller while a body is recorded. Values stay in
clear text in the plan; base64 is not encryption.

### Providers

| Provider | Meaning |
|----------|---------|
| `secret.FileProvider{}` (default) | Reads `secrets/<ref>` below the working directory. Bytes exact. A leading `/` stays inside `secrets/`. Symlinks, escapes and non-regular files are refused. `Dir: "vault"` picks another single directory. |
| `secret.NewSnapshot(p)` | Resolve each reference at most once per process; caches values and not-found. Wrap every non-file provider in it. |
| `secret.NewFallback(primary, secondary)` | Ask `secondary` only when `primary` says not-found. Any other `primary` failure is returned. For migrating one secret at a time. |
| `foostore.New(foostore.Config{Lookup: items})` | Runs the `foostore` binary against a KeePass store. `foostore.Items(map)` maps refs to `foostore.Field(entry, field)` or `foostore.Attachment(path)`. Unmapped refs are not-found. |
| `secret.ProviderFunc` | Adapt a function. Implement `Resolve(ctx, secret.Ref) ([]byte, error)`. |

```go
api.SetSecretProvider(secret.NewSnapshot(secret.NewFallback(vault, secret.FileProvider{})))
```

Error kinds (`*secret.Error`): `ErrNotFound`, `ErrInvalid`, `ErrUnreadable`,
`ErrUnavailable`. Decide with `secret.IsNotFound(err)` on the returned error,
not `errors.Is`. A missing `secrets/` directory is `ErrUnavailable`, so
`OptionalSecret` fails instead of silently dropping fragments.

foostore details: timeout 30s (`Config.Timeout`), value cap 16 MiB, no
terminal and only `HOME` in the child environment, passphrase via
`kdbx_pass_file` or `Config.Passphrase` over fd 3. `~/.config/foostore.json`
must set `kdbx_pass_file`; the pass file must be `0600` or `0400`.

### Sensitive ops

Every resolved value is remembered. While recording, every string of every
op is scanned for it (verbatim, trimmed, or without a trailing newline):

| Field class | Examples | On a match |
|-------------|----------|-----------|
| payload | content, template data, lines, argv, env, cron command, validator args | op marked `sensitive` |
| identity | IDs, names, paths, link targets, binaries, deps | strong secret: record refused; weak: marked |
| metadata | kind, mode, owner, group, shell | strong: marked; weak: ignored |

- Strong: at least 8 bytes after trimming and not word-like. Secrets under
  4 bytes match only as a whole payload value.
- The usual refusal is an unnamed `Command` whose argv holds a secret: give
  it `WithName`.
- Not scanned: synced trees, transformed values (base64, hashes, splits),
  task names, descriptions, host values. Mark such payloads with
  `WithSensitive`; keep secrets out of names.

What a sensitive op changes:

- `plan -stdout` refuses; `-with-secrets` overrides.
- `plan -redacted` withholds every payload string of the op.
- `plan -o` seals by default when a recipients file exists, else warns.
- On the destination: validator output, template error details, command
  argv and output, package-manager output are withheld (sizes only).
- Debug logs never print content digests.

Controller-side redaction covers log lines, the apply summary, CLI errors,
push summaries, and the relayed output of the elevated child and the remote
gonf (line by line, multi-line secrets included). After the relayed process
exits, output from a lingering descendant is handed to a detached `cat`
after 2s and is not redacted.

Limits: argv is visible in the destination's process list; written files
and crontab lines hold the secret in clear text; Go memory is not zeroed.

## Sealed plans

Encrypt a plan at rest with [age](https://age-encryption.org), hybrid
post-quantum recipients only.

```text
age-keygen -pq -o ~/.config/gonf/identity
age-keygen -y ~/.config/gonf/identity >> ~/.config/gonf/recipients

gonf plan -o out -seal frontends_nsd                    # out/plan.age
gonf apply -identity ~/.config/gonf/identity out/plan.age
gonf plan -seal -stdout t | ssh host doas gonf apply -identity /etc/gonf/identity -
age -d -i key.txt out/plan.age | gonf apply -           # emergency path
```

| File | Default | Rules |
|------|---------|-------|
| identity | `${XDG_CONFIG_HOME:-$HOME/.config}/gonf/identity` (non-root) | regular file, yours, no group/other bits, no symlink |
| recipients | `${XDG_CONFIG_HOME:-$HOME/.config}/gonf/recipients` | one `age1pq...` per line, `#` comments; yours, not group/other-writable, no symlink |

- The artifact is age over the GONF-PUSH/1 frame. No plan schema change.
  No `plan.jsonl` or `blobs/` is written.
- Recipients: union of `-recipient` and the recipients file. Zero
  recipients is refused, never plaintext. Classic `age1...`, `ssh-...` and
  plugin recipients are refused.
- The report prints each recipient as a short form plus `sha256:`
  fingerprint (full keys with `-verbose`). Check it.
- Seal by default: `plan -o dir` with a sensitive plan and an existing
  recipients file writes `dir/plan.age`. An existing but unusable
  recipients file refuses the plan. `-plaintext` opts out. `-stdout`,
  `-for` and `-sign` still need an explicit `-seal`.
- Applying: the whole decrypted frame (capped at 512 MiB) is read before
  any op runs. Blobs extract into a private `sealed-run-*` dir that is
  removed afterwards; dead owners' leftovers are swept by the next sealed
  apply.
- Output says `decrypted and applied plan.age (N ops)`. Decryption proves
  nothing about who made the file: recipients are public.
- No forward secrecy. Delete artifacts once applied and rotate keys at
  least yearly, when a host is rebuilt, and on suspected compromise.
- Older gonf refuses a sealed file before any op. Check with
  `gonf -sealed-version`.

### Per-host artifacts (`-for`)

```text
ssh rex@blowfish sudo age-keygen -pq -o /etc/gonf/identity   # on the host
# Host("blowfish", ..., WithPlanRecipient("age1pq1..."))     # inventory
gonf plan -o out -seal -for blowfish frontends_nsd            # out/plan-blowfish.age
scp out/plan-blowfish.age rex@blowfish:
ssh rex@blowfish sudo gonf apply -identity /etc/gonf/identity plan-blowfish.age
```

- Records once per host with that host's `ForHosts` selection; seals to the
  host's recipient plus your own recipients.
- Refused before writing anything when a host has no recipient, your own
  recipient set is empty, or two host names sanitize (`[A-Za-z0-9._-]`) to
  the same file name. All artifacts are staged and published together.
- With `-stdout` it must resolve to exactly one host.
- The selection is substring-based: a host whose name is a substring of
  another's ends up in its artifact.
- Generate destination keys on the destination. Never copy a private key.

## Signed plans

Ed25519 signature around a sealed artifact. Optional; unsigned `plan.age`
stays valid for interactive apply.

```text
gonf plan-signer-keygen ~/.config/gonf/signer >> trusted-signers-for-hosts
gonf plan -o out -seal -sign ~/.config/gonf/signer frontends_nsd
gonf apply -identity id -trusted-signers /etc/gonf/signers -require-signed out/plan.age
gonf plan-verify -trusted-signers f out/plan.age | age -d -i key | gonf apply -
```

| File | Format | Rules |
|------|--------|-------|
| signer | `GONF-SIGNER-SECRET-ED25519 <base64 seed>` line, created `0600` | yours, no group/other bits; no default path |
| trusted-signers | `gonf-signer-ed25519 <base64 key> [label]` per line, `#` comments | yours, not group/other-writable, no symlink; at least one entry |

- Envelope: `GONF-SIGNED-PLAN/1`, key, signature, `signed-at <RFC 3339 UTC>`,
  then the `plan.age` bytes. The signature covers the magic, the time and
  the ciphertext.
- `-sign` needs `-seal`. With `-for` every host's artifact is signed
  separately. The report adds `signed <time>` and the signer fingerprint.
- Verification order on apply: sniff, load trusted signers, verify the
  signature, check freshness, and only then decrypt. Any failure: exit 1,
  nothing decrypted.
- A signed plan is always verified, flags or not. Root must pass
  `-trusted-signers`.
- Freshness: refused when `signed-at` is older than `-max-signed-age`
  (default 24h) or more than 5 minutes in the future.
- `-require-signed` refuses unsigned `plan.age`, `plan.jsonl`, push frames
  and bare JSONL. With only `-trusted-signers`, unsigned input applies with
  a warning.
- Small-order and non-canonical keys are refused. Revoke a signer by
  removing its line on each destination. Re-sealing needs a new signature.
- Older gonf (before 0.18.0) refuses a signed file. Check with
  `gonf -signed-version`.
- Not provided: anti-replay counters, threshold signing, any unattended
  apply entry point. Sealed plans are applied only by an operator choosing
  the file.

## Error handling

| Class | When | What happens |
|-------|------|--------------|
| declaration error | DSL misuse: duplicate names or IDs, bad option values or combinations, unsupported option, bad companions, failed `Must*` lookups, `ForHosts` misuse, `SetSecretProvider` misuse | Reported, the constructor returns an inert value, the recipe keeps running so later errors are still checked. |
| record error | unknown task, cycle, packaging failure, dangling or cross-chunk deps, failing aggregate member or nested `Run`, secret failure | `RecordPlan`/`Run`/push fail; nothing is applied or pushed; temp dirs removed. |
| push refusal | opaque-only task in a pushed plan, `-privilege=none` with elevated ops | Returned before any SSH traffic. |
| apply error | a resource fails on the destination | Returned up to the CLI, exit 1, cleanup runs. |

- The first declaration error wins and carries the recipe line
  (`declared at <file:line>`).
- Inside a recording it fails that record. Outside, it is sticky for the
  process: `RecordPlan`, `Run`, `Apply` and every CLI invocation
  (`-list` and `-version` included) refuse with it, exit 1.
- `resource.ResetDeclarationError()` clears the sticky error and returns
  it. Safe to continue after a duplicate ID. After a failed secret lookup a
  resource already holds the empty value: also call
  `resource.ResetRepository()` and declare everything again.
- `api.Apply` refuses after any failed record until a later record
  succeeds.
- Library code never exits the process; only `cli.CLI()`'s caller does.

## Go API

| API | Meaning |
|-----|---------|
| `cli.Main()` | The full CLI, exiting with its code; the last line of `main`. |
| `cli.CLI() int` | The same, returning the exit code instead. |
| `Run`, `RunContext` | Record and apply. |
| `RecordPlan(id, dir, tasks...)` | Record into `dir` with the output-directory rules. |
| `RecordPlanTo(id, store, tasks...)` | Record into a caller-owned `plan.BlobStore`. |
| `RecordPlanForHost(host, id, store, tasks...)` | Record with one host's `ForHosts` selection. |
| `ApplyPlan`, `ApplyPlanContext` | Apply one chunk (no pre-flight, no privilege split). |
| `ApplyChunks`, `ApplyChunksContext` | Apply a recorded plan with the privilege split. |
| `Apply()` | Apply resources registered outside tasks, through the plan engine, with the privilege split. |
| `PushTo`, `PushHost`, `PushCluster`, `PushClusterRun`, `PushFleet`, `PushFleetRun` | Push (the `*Run`/`*Context` forms take a context, plan id and parallelism override). |
| `PreviewTo`, `PreviewHost`, `PreviewClusterRun`, `PreviewFleetRun` | Strict preview. |
| `PushPayload`, `PushPayloadContext` | Ship an encoded payload as one chunk. |
| `SetCommandTimeout`, `SetPrivilege`, `SetProfileOverride` | Programmatic forms of the flags. |
| `RedactSecrets(s)`, `SensitiveOpNames(ops)` | Redaction helpers. |
| `ResetTasks`, `ResetInventory`, `ResetForTest` | Clear registries. |

Record one plan at a time: recording is single-goroutine.

## Build and test

```text
go build ./...
go install ./cmd/gonf            # or: mage install
go test ./...
mage test                        # go test -race -shuffle=on -v -count=1 ./...
mage lint                        # gofmt check, go vet, staticcheck
```

Live tests are opt-in: `GONF_RUN_TIMER_TESTS=1`, `GONF_RUN_CRON_TESTS=1`,
`GONF_RUN_BSD_SERVICE_TESTS=1` (see the resource docs in
[design/](design/README.md)).

## Release checklist

- [ ] Bump `internal.Version`. In the same commit, update the version and
      schema lines in [design/conf-rex-gaps.md](design/conf-rex-gaps.md)
      and add the [CHANGELOG](../CHANGELOG.md) entry.
- [ ] If `plan.CurrentVersion` moved, add the schema paragraph to
      [design/plan.md](design/plan.md) and the row to the schema table
      above.
- [ ] Grep the docs for "unreleased" and dev branch names; re-wrap any
      paragraph edited with `sed`.
- [ ] Run `gofmt -l .`, `go build ./...`, `go vet ./...`,
      `go test -race -shuffle=on -count=1 ./...`, `go tool staticcheck ./...`.
- [ ] Tag only after the doc edits are on the tagged branch, with the
      CHANGELOG entry as the annotated tag message. A pushed tag cannot be
      fixed.
