# File, Dir, and Link resources

Core filesystem resources. Paths may be a single `string` or `List(...)`.

```go
File("/etc/motd", WithContent("hello\n"), WithMode(0o644))
File("/etc/app.conf", WithSource("assets/app.conf.tmpl"))
File("/etc/lines.conf", WithLines("keep=1", "other=1"), WithoutLines("stale", "obsolete"))
File("/etc/rc.conf.local", WithLine(`httpd_flags=""`), WithName("rc-conf-httpd-flags"))
EnsureFile("/etc/daily.local", WithMode(0o644))
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
| `WithTemplateData` | File | JSON-compatible map, slice, or struct made available to destination-side templates; also enables rendering |
| `WithValidation` | File | Validate one rendered candidate before publishing it; see below |
| `WithSource` | File / Dir | Copy from path or template |
| `WithSourceGlob` | Dir | Install glob matches into the directory by basename |
| `WithLines` / `WithoutLines` | File | Ensure / remove lines in declaration order (duplicates are ignored); singular `WithLine` / `WithoutLine` remain compatibility wrappers |
| `WithName` | File / Command | Explicit resource identity. A named File keeps managing its supplied path but is registered and reported as `File[name]`, allowing separate line edits to one file and precise `DependsOn` / `OnChange` wiring. Without it, file IDs remain `File[path]` and duplicate registrations still fail. |
| `WithOwner` / `WithGroup` / `WithMode` | File / Dir | Ownership and mode. Recorded in plan ops (`owner`/`group`, schema v4) and enforced on apply; owner is a user name, group is a numeric gid or group name (resolved via `os/user`). Only explicitly set ownership is recorded — the build-time default (current user) is not pushed to remote hosts, and absent files carry no ownership. `WithMode` accepts setuid/setgid/sticky: either raw octal (e.g. `0o4755`) or Go flag form (`0o755\|os.ModeSetuid`); both normalize to the flag form, lower to a four-digit plan wire mode (`"04755"`), and land on disk (apply chowns before it chmods so unprivileged chown cannot clear the special bits). Bits above `0o7777` are rejected. Modes without owner-read (e.g. `0o000`) are applied on non-root runs too: the attribute step falls back to path-based `chmod`/`chown` when the descriptor-based open is denied (never through a symlink at the target). |
| `WithFileMode` | Dir | Mode for files created from a source tree (same setuid/setgid/sticky handling as `WithMode`) |
| `WithPrune` | Dir | Remove unexpected children when syncing / absent |
| `WithSymlink` / `WithHardlink` | Link | Link target (Link does not take owner/mode options) |
| `IsAbsent` / `No*` | all | Ensure missing |
| `DependsOn` | all | Apply after other resources |

Helpers that wrap these: [helpers.md](helpers.md) (`InstallFile`, `SyncDir`, `EnsureDir`, `EnsureFile`, `LinkIfExists`, `SymlinkMap`). `EnsureFile` creates an empty regular file only when absent; existing regular files retain their content while explicitly supplied mode, owner, and group converge.

### Single-file candidate validation

`WithValidation` makes an absolute `File` with explicit `WithContent` or
`WithSource` render once, write a fresh `0600` candidate next to the live
target, run a validator using argv, and only then publish through the normal
atomic single-file rename. Put
`CandidatePath` exactly once in the argument list; it is supplied by Gonf, so
recipes do not construct or interpolate a temporary filename.

```go
config := File("/etc/httpd.conf", WithContent(rendered),
    WithValidation("httpd", List("-n", "-f", CandidatePath)))
Service("httpd", WithRestart, OnChange(config))
```

Validation runs before every non-dry-run reconciliation, including repair of
manually changed live content. A failed validator removes its candidate, leaves
the live file untouched, and reports no `File` change, so an `OnChange` restart
does not run. Candidates are unique per apply and cleaned after both success
and failure; their private mode is independent of the final file mode.

This is deliberately a **single-file** contract. The candidate shares the live
file's parent directory, which preserves ordinary relative-path resolution, but
that directory must be owned by the applying uid and have no group or
other-write permission. Every ancestor must likewise be owned by root or the
applying uid; group/other-writable ancestors also need sticky protection (as
with `/tmp`). Symlinks and `..` path components are refused. Otherwise another
user could replace the
candidate after it is closed and before the validator opens it; Gonf refuses
such a path rather than treating a `0600` candidate as sufficient protection.
It cannot safely validate a configuration whose includes, chroot, or companion
files must move together. Use a future multi-file configuration resource for
those cases.
Concurrent validations use separate candidates, but concurrent publishes to
the same target are not serialized: each is atomic individually and the last
successful rename wins. Several files are likewise not an atomic transaction;
a failure after one publication requires a later apply or explicit recovery to
converge the set.

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

### Template data and destination facts

`WithTemplateData` carries JSON-compatible maps, slices, and structs on the
plan wire and renders them only on the destination. Map keys are available at
the template root and every supplied value is also available as `.Data` (so
non-map values use `.Data`). Existing environment variables and `.Param` stay
available. `.Gonf` is reserved for live destination facts:
`.Gonf.GOOS`, `.Gonf.Profile`, and `.Gonf.Hostname`.

On plan apply these facts are detected on the destination per
`api.ApplyPlan` call (i.e. per privilege chunk). On local applies
(`gonf -profile=... <task>`, `gonf -profile=... apply plan.jsonl`, and their
elevated re-exec) they honour the CLI `-profile` override, so
`.Gonf.Profile` is the overridden profile. On `push` / `fleet` the remote
`gonf apply` is not passed `-profile`, so the destination renders from its
own detected facts. `.tmpl` entries inside a
`SyncDir` / `Dir(..., WithSource(...))` tree render them identically to a
single `File` with the same template text in the same apply. Only the
deprecated direct (non-plan) resource path still detects the facts locally
per file and ignores the `-profile` override.

The stable string helpers are `join`, `lower`, `upper`, `trim`, and `replace`.
Templates use `missingkey=error`; missing map keys fail the apply rather than
silently rendering an empty value.

### Template rendering for a single `File` on the plan path

A single `File(dst, WithSource("app.conf.tmpl"))` (not part of a `SyncDir`
tree) is rendered the same way whether applied directly or through
`Run`/`push`/`cluster`/`fleet` — everything goes through `api.Run` ->
`RecordPlan` + `plan.Apply`. The `.tmpl` suffix never reaches the
destination as a suffix (`planDraft` records the already-stripped
destination path, and `RecordPlan` packages the source's raw bytes into
`content_b64`/`blob`), so the `file` op instead carries the intent
explicitly: `template:true` plus `template_param` set to the recipe's
declared source path (schema v9). Plan apply forces rendering
(`opt.WithTemplate`) and reproduces the same `{{.Param}}` a direct run would
(`opt.WithParam(template_param)`) before writing the destination file. A
`SyncDir`/`Dir` source tree is unaffected by this — see the section above —
because its per-file copies always carry a real, still-`.tmpl`-suffixed
`WithSource` at apply time.

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
