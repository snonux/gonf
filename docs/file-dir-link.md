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
| `WithOwner` / `WithGroup` / `WithMode` | File / Dir | Ownership and mode. Recorded in plan ops (`owner`/`group`, schema v4) and enforced on apply; owner is a user name, group is a numeric gid or group name (resolved via `os/user`). Only explicitly set ownership is recorded — the build-time default (current user) is not pushed to remote hosts, and absent files carry no ownership. `WithMode` accepts setuid/setgid/sticky: either raw octal (e.g. `0o4755`) or Go flag form (`0o755\|os.ModeSetuid`); both normalize to the flag form, lower to a four-digit plan wire mode (`"04755"`), and land on disk (apply chowns before it chmods so unprivileged chown cannot clear the special bits). Bits above `0o7777` are rejected. Modes without owner-read (e.g. `0o000`) are applied on non-root runs too: the attribute step falls back to path-based `chmod`/`chown` when the descriptor-based open is denied (never through a symlink at the target). |
| `WithFileMode` | Dir | Mode for files created from a source tree (same setuid/setgid/sticky handling as `WithMode`) |
| `WithPrune` | Dir | Remove unexpected children when syncing / absent |
| `WithSymlink` / `WithHardlink` | Link | Link target (Link does not take owner/mode options) |
| `IsAbsent` / `No*` | all | Ensure missing |
| `DependsOn` | all | Apply after other resources |

Helpers that wrap these: [helpers.md](helpers.md) (`InstallFile`, `SyncDir`, `EnsureDir`, `LinkIfExists`, `SymlinkMap`).

### Template `{{.Param}}` in synced trees

A file rendered from a `.tmpl` source may reference `{{.Param}}`: the source
identity the file was configured from. For a single file that is the recipe's
declared source path; for a `.tmpl` entry inside a `SyncDir` tree it is the
declared source directory + "/" + the entry's path relative to the tree root
(for the glob flavor: the declared glob pattern's directory + "/" + the
basename). On the plan path the identity travels on the `sync_dir` op as
`source_dir` (schema v6): plan apply passes it to the synced tree, so the
rendered content never embeds the ephemeral `planDir/blobs/…` extraction
path — which changes every plan run and would flap the file's checksum.
Direct (non-plan) use derives the same stable value from the real source
tree; plans recorded before schema v6 keep the old blob-path Param.
`opt.WithParam` overrides the value explicitly (plan-engine plumbing).

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

The move itself is kernel-enforced to be non-destructive for everything but
directories: for files, symlinks, and other non-directories (on non-darwin
platforms) the aside is created with link(2) as a hard link to the entry,
which fails atomically with EEXIST instead of overwriting when an entry
appears at `path.old` between the pre-check and the move — a backup planted
there can never be clobbered. For a brief moment the entry is reachable
under both names, and an interrupted move leaves both; the next apply then
refuses on the leftover backup instead of destroying data. link(2) cannot
hardlink directories, and on macOS it follows symlinks, so directories —
and every entry on darwin — fall back to a plain rename: a non-empty
directory planted at `path.old` then fails the conversion loudly, while a
planted empty one is lost (the accepted residual race).

See also [examples/examples.go](../examples/examples.go).
