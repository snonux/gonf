# Consumer helpers

Convenience wrappers used heavily by laptop/dotfiles configs. Import
`github.com/snonux/gonf/api` (often with a dot-import).

## Paths

```go
Home(".config", "app")     // $HOME/.config/app
Expand("~/bin/tool")       // expand leading ~
List("a", "b", "c")        // DSL []string alias — prefer over []string{"a","b","c"}
```

At **apply** time, plan paths may also use `${HOME}` (expanded on the
destination). See [plan.md](plan.md).

## Cluster hosts and per-host values

```go
Host("web", WithSSHHost("web.example"),
    WithValue("cron", [2]string{"6", "7"}))
Cluster("edge", web, …)

RegisterMethods(MyTasks{}, WithPrefix("edge_"), WithCluster("edge"))

func (MyTasks) Cron() {
    for _, host := range ClusterHosts() {           // hosts of WithCluster
        w := MustHostValue[[2]string](host, "cron") // declaration error if missing/wrong type
        WhenHostname(host, func() { /* … */ })
    }
}
```

`ClusterHosts` only works inside a task registered with `WithCluster` /
`WithTaskCluster`. Prefer `WithValue` / `SetValue` on the host over a parallel
hostname→value map in the recipe.

### Typed host iterator: `ForHosts`

`ForHosts[T](key, fn)` is the one-call form of the loop above. `T` is
inferred from `fn`:

```go
func (MyTasks) Cron() {
    ForHosts("cron", func(host string, w [2]string) {
        File("/etc/cron-window", WithContent(w[0]+" "+w[1]+"\n"))
        // Per-host inputs are read inside fn (see "Selected-host inputs").
        File("/etc/upload.token", WithContent(MustSecret("tokens/"+host)),
            WithMode(0o600))
    })
}
```

Semantics, in order:

1. Hosts are the current task's `WithCluster` members in registration order.
   Inventory is the only source of the values.
2. Every member's value under `key` is looked up and type-checked **before**
   any fragment is recorded, even for a host that this run does not target
   (inventory values are public data, so an inventory bug fails the same way
   for every target).
3. `fn(host, value)` runs inside `WhenHostname(host, …)`. When a plan is
   recorded this adds a `hostname_contains` guard that the destination
   evaluates at apply time, not the controller. Outside recording, `fn` runs
   only when the local hostname contains `host`.
4. **Errors.** While a plan is recorded (`gonf <task>`, `gonf plan`, push,
   cluster, fleet), an empty key, a nil `fn`, a task without `WithCluster`,
   or a missing or mistyped value fails the record with an error, like
   `MustSecret`: nothing is applied or pushed, no SSH connection is opened,
   and a local run removes its temporary plan directory. A direct call
   outside any recording reports the same declaration error, which `Apply`
   and the CLI refuse to run with.
5. **Selected-host inputs.** Entry points that know where the plan applies
   record with a host selection. `fn` is not called for members outside it,
   so their per-host secrets are never read:

   | Entry point | Hosts visited by `ForHosts` |
   | --- | --- |
   | `PushHost(h)` / `PreviewHost(h)` | `h` and every name that could match the same machine (see below) |
   | `gonf push user@host` / `PushTo` / `PreviewTo` | an exact inventory destination: hosts whose SSH host or inventory name equals it and whose explicit user and port (if set on both sides) agree, plus names that could match the same machine; entries with a contradicting user or port are skipped. **Every member** when raw ssh arguments are given (`gonf push -- -p 2222 …`, `-o HostName=…`) or no entry matches exactly |
   | `gonf cluster` / `PushClusterRun` / `PreviewClusterRun` | the cluster's members, plus names that could match them |
   | `gonf fleet` / `PushFleetRun` / `PreviewFleetRun` | the fleet's deduplicated members, plus names that could match them |
   | local `gonf <task>` / `Run` | the inventory names the local hostname contains (exactly the fragments the local apply accepts) |
   | `gonf plan`, `RecordPlan` | every member (plan output unchanged) |

   "Names that could match the same machine" are aliases sharing its SSH host
   at a compatible port (`r0` and `r0-wg` both on `r0.lan`; SSH hosts and
   push destinations compare case-insensitively, so `Host5.lan` and
   `host5.lan` are one SSH host; an unset port is compatible with any port),
   plus names contained in any inventory name or
   SSH host of the machine, including its aliases (`pi1` for `pi10`), since
   the destination guard is a substring test on the live hostname. So every
   name of one machine yields the same selection: if `db` and `pi10` share
   `10.0.0.5`, pushing `db`, `pi10` or `10.0.0.5` all select `db`, `pi10`
   and `pi1`. Hosts on one SSH host with different explicit ports
   (`vm1` on `f3.lan:2201`, `vm2` on `f3.lan:2202`) are different machines:
   pushing to `vm1` does not read `vm2`'s inputs.
   Aggregates inherit the selection of the recording that runs them.

Read host-specific secrets inside `fn`, not before `ForHosts`, so a
single-host push needs only that host's secret. The live hostname is only
known on the destination, so push narrowing assumes that each machine's live
hostname contains no inventory name other than those appearing in its own
inventory name or SSH host, and that one machine is registered under one SSH
host (in any letter case). A machine registered as both `203.0.113.5` and `fishfinger.buetow.org`
breaks this: a push to one name may skip the other name's fragment. If that
does not hold, push with raw ssh arguments or through an unregistered
destination to record every member. Local runs use the real hostname and are
exact. `ClusterHosts` and
`WhenHostname` do not use the selection, so existing
`WhenHostname(ClusterHosts(), …)` recipes record the same plans as before.
Recording is single-goroutine: never record two plans concurrently.

## Install / sync

```go
InstallFile(Home(".gitconfig"), "assets/gitconfig")
// default mode 0640; later WithMode wins
// plan-record packages file bytes as content_b64 (or a blob if large)

SyncDir(Home(".config/app"), "assets/app/*", WithMode(0o755), WithFileMode(0o644))
// defaults: dir 0700, files 0640 before extra opts
// plan-record copies the tree under planDir/blobs/

EnsureDir(Home(".local/bin"), WithMode(0o755))
// plan-record → ensure_dir recipe (destination decides)
// outside plan-record → Dir only if path missing / not a directory
```

`InstallFile` → `File(..., WithSource(...))`.  
`SyncDir` → `Dir(..., WithSourceGlob(...))` — each glob match is installed
under the destination by **basename only** (no relative subdirectory tree).

## Symlinks

```go
LinkIfExists(Home("bin/foo"), "/opt/foo/bin/foo")
// plan-record → link_if_exists recipe
// outside plan-record → target exists ? symlink : NoLink

SymlinkMap(Home("bin"),
    "foo", "/opt/foo/bin/foo",
    "bar", "/opt/bar/bin/bar",
)
```

`SymlinkMap` takes alternating `name, target` strings under `parent`.

## Path gates

```go
WhenPathExists(Home("Notes/prompts/commands"), func() {
    EnsureDir(Home(".cursor"), WithMode(0o750))
    Link(Home(".cursor/commands"), WithSymlink(Home("Notes/prompts/commands")))
})
```

In plan-record mode this emits `when_begin` / `path_exists` / `when_end`
around the body so the destination can decide. Outside plan-record it
probes the local filesystem immediately.

## Predicates

```go
When(And(
    func(f Facts) bool { return f.GOOS == "linux" },
    ProfileIs("fedora"),
))
When(Or(ProfileIs("fedora"), ProfileIs("rocky")))
```

Prefer serializable helpers (`WhenLinux`, `WhenProfile`,
`WhenHostnameContains`) when configs must round-trip through a plan.
Opaque `When(func…)` cannot be encoded in JSONL.

`WhenLinux()` is a `TaskOption` (not a `func(Facts) bool`); use it as
`Task(..., WhenLinux())` or pass `func(f Facts) bool { return f.GOOS == "linux" }`
into `And` / `Or`.

## Key/value

```go
pairs, err := ParseKV(List("user.name", "Ada", "user.email", "ada@example.com"))
EachKV(List("user.name", "Ada", "user.email", "ada@example.com"),
    func(k, v string) { /* … */ })
```

`EachKV` reports a declaration error on odd-length lists (and calls `fn` for
none of them); `ParseKV` returns the error.

## Git

```go
GitGlobal(
    "user.name", "Ada",
    "user.email", "ada@example.com",
)
```

Registers `Command("git", …)` entries for `git config --global`.
