# Consumer helpers

Convenience wrappers used heavily by laptop/dotfiles configs. Import
`github.com/snonux/gonf/api` (often with a dot-import).

## Paths

```go
Home(".config", "app")     // $HOME/.config/app
Expand("~/bin/tool")       // expand leading ~
List("a", "b", "c")        // []string{"a","b","c"}
```

## Install / sync

```go
InstallFile(Home(".gitconfig"), "assets/gitconfig")
// default mode 0640; later WithMode wins

SyncDir(Home(".config/app"), "assets/app/*", WithMode(0o755), WithFileMode(0o644))
// defaults: dir 0700, files 0640 before extra opts

EnsureDir(Home(".local/bin"), WithMode(0o755))
// registers Dir only if path is missing or not a directory; else empty Multi
```

`InstallFile` → `File(..., WithSource(...))`.  
`SyncDir` → `Dir(..., WithSourceGlob(...))` — each glob match is installed
under the destination by **basename only** (no relative subdirectory tree).

## Symlinks

```go
LinkIfExists(Home("bin/foo"), "/opt/foo/bin/foo")
// target exists → symlink; missing → NoLink (ensure path absent)

SymlinkMap(Home("bin"),
    "foo", "/opt/foo/bin/foo",
    "bar", "/opt/bar/bin/bar",
)
```

`SymlinkMap` takes alternating `name, target` strings under `parent`.

## Predicates

```go
When(And(
    func(f Facts) bool { return f.GOOS == "linux" },
    ProfileIs("fedora"),
))
When(Or(ProfileIs("fedora"), ProfileIs("rocky")))
```

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
