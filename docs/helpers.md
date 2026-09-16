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
        w := MustHostValue[[2]string](host, "cron") // Fatal if missing/wrong type
        WhenHostname(host, func() { /* … */ })
    }
}
```

`ClusterHosts` only works inside a task registered with `WithCluster` /
`WithTaskCluster`. Prefer `WithValue` / `SetValue` on the host over a parallel
hostname→value map in the recipe.

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

`EachKV` fatals on odd-length lists; `ParseKV` returns an error.

## Git

```go
GitGlobal(
    "user.name", "Ada",
    "user.email", "ada@example.com",
)
```

Registers `Command("git", …)` entries for `git config --global`.
