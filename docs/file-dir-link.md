# File, Dir, and Link resources

Core filesystem resources. Paths may be a single `string` or `List(...)`.

```go
File("/etc/motd", WithContent("hello\n"), WithMode(0o644))
File("/etc/app.conf", WithSource("assets/app.conf.tmpl"))
File("/etc/lines.conf", WithLine("keep=1"), WithoutLine("stale"))
NoFile("/tmp/old.txt")

Dir("/var/lib/app", WithMode(0o755))
Dir("/opt/tree", WithSource("assets/tree"), WithPrune, WithFileMode(0o644))
NoDir("/tmp/stale", WithPrune)

Link("/usr/local/bin/tool", WithSymlink("/opt/tool/bin/tool"))
Link("/var/lib/app/data", WithHardlink("/data/app"))
NoLink("/tmp/stale-link")
```

## Options (filesystem)

| Option | Applies to | Meaning |
|--------|------------|---------|
| `WithContent` | File | Inline body (templates: env + `.Param` via source `.tmpl`) |
| `WithSource` | File / Dir | Copy from path or template |
| `WithSourceGlob` | Dir | Glob sources into the directory |
| `WithLine` / `WithoutLine` | File | Ensure / remove a line |
| `WithOwner` / `WithGroup` / `WithMode` | File / Dir | Ownership and mode |
| `WithFileMode` | Dir | Mode for files created from a source tree |
| `WithPrune` | Dir | Remove unexpected children when syncing / absent |
| `WithSymlink` / `WithHardlink` | Link | Link target (Link does not take owner/mode options) |
| `IsAbsent` / `No*` | all | Ensure missing |
| `DependsOn` | all | Apply after other resources |

Helpers that wrap these: [helpers.md](helpers.md) (`InstallFile`, `SyncDir`, `EnsureDir`, `LinkIfExists`, `SymlinkMap`).

See also [examples/examples.go](../examples/examples.go).
