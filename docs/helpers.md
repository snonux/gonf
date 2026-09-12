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
SyncDir(Home(".config/app"), "assets/app/*", WithMode(0o755), WithFileMode(0o644))
EnsureDir(Home(".local/bin"), WithMode(0o755))
```

`InstallFile` → `File(..., WithSource(...))`.  
`SyncDir` → `Dir(..., WithSourceGlob(...))`.

## Symlinks

```go
LinkIfExists(Home("bin/foo"), "/opt/foo/bin/foo") // no-op if target missing
SymlinkMap(Home("bin"),
    "foo", "/opt/foo/bin/foo",
    "bar", "/opt/bar/bin/bar",
)
```

`SymlinkMap` pairs are `name, target` via `List`/`EachKV`-style alternating strings.

## Predicates

```go
When(And(WhenLinux(), ProfileIs("fedora")))
When(Or(ProfileIs("fedora"), ProfileIs("rocky")))
```

## Key/value iteration

```go
EachKV(List("user.name", "Ada", "user.email", "ada@example.com"),
    func(k, v string) { /* … */ })
```

## Git

```go
GitGlobal(
    "user.name", "Ada",
    "user.email", "ada@example.com",
)
```

Registers `Command("git", …)` entries for `git config --global`.
