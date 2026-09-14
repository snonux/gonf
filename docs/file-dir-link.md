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
| `WithSourceGlob` | Dir | Install glob matches into the directory by basename |
| `WithLine` / `WithoutLine` | File | Ensure / remove a line |
| `WithOwner` / `WithGroup` / `WithMode` | File / Dir | Ownership and mode. Recorded in plan ops (`owner`/`group`, schema v4) and enforced on apply; owner is a user name, group is a numeric gid or group name (resolved via `os/user`). Only explicitly set ownership is recorded — the build-time default (current user) is not pushed to remote hosts, and absent files carry no ownership. `WithMode` accepts setuid/setgid/sticky: either raw octal (e.g. `0o4755`) or Go flag form (`0o755\|os.ModeSetuid`); both normalize to the flag form, lower to a four-digit plan wire mode (`"04755"`), and land on disk (apply chowns before it chmods so unprivileged chown cannot clear the special bits). Bits above `0o7777` are rejected. |
| `WithFileMode` | Dir | Mode for files created from a source tree (same setuid/setgid/sticky handling as `WithMode`) |
| `WithPrune` | Dir | Remove unexpected children when syncing / absent |
| `WithSymlink` / `WithHardlink` | Link | Link target (Link does not take owner/mode options) |
| `IsAbsent` / `No*` | all | Ensure missing |
| `DependsOn` | all | Apply after other resources |

Helpers that wrap these: [helpers.md](helpers.md) (`InstallFile`, `SyncDir`, `EnsureDir`, `LinkIfExists`, `SymlinkMap`).

### Replacing real entries with links (the `.old` aside)

When `Link` must replace an existing real file, directory, or non-matching
entry with a symlink or hardlink, the existing entry is first moved aside to
`path.old` while the link is created. The aside is removed as soon as the link
exists, so a completed conversion leaves no residue. If the link cannot be
created, the original entry is moved back from `path.old` to `path` and the
apply fails with the original error. If even the restore fails, the error
says so and the user's entry remains available under `path.old`. If the
aside cannot be removed after a successful create (the old entry was a
non-empty directory), the link is in place but the apply reports an error
naming the backup path instead of deleting the user's data; a later apply
then recognizes the already-created link and succeeds without touching the
backup.

If `path.old` already exists before the conversion — file, directory, or
symlink, even a dangling one — the operation refuses with an error and changes
nothing; remove or rename the stale backup manually first. Dry-runs surface
the same refusal. Repointing an existing symlink to a new target never uses
the `.old` aside.

See also [examples/examples.go](../examples/examples.go).
