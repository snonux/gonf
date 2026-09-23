# File, Dir, and Link resources

Core filesystem resources. Paths may be a single `string` or `List(...)`.

```go
File("/etc/motd", WithContent("hello\n"), WithMode(0o644))
File("/etc/app.conf", WithSource("assets/app.conf.tmpl"))
File("/etc/lines.conf", WithLines("keep=1", "other=1"), WithoutLines("stale", "obsolete"))
File("/etc/rc.conf.local", WithLine(`httpd_flags=""`), WithName("rc-conf-httpd-flags"))
File("/root/.profile", WithKeyedLine("export PKG_PATH=", `export PKG_PATH="https://repo/"`), WithMode(0o644), WithOwner("root"), WithGroup("wheel"))
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
| `WithKeyedLine(key, line)` | File | Own the one line starting with the literal prefix `key` in a shared file; see below |
| `WithName` | File / Command | Explicit resource identity. A named File keeps managing its supplied path but is registered and reported as `File[name]`, allowing separate line edits to one file and precise `DependsOn` / `OnChange` wiring. Without it, file IDs remain `File[path]` and duplicate registrations still fail. |
| `WithOwner` / `WithGroup` / `WithMode` | File / Dir | Ownership and mode. Recorded in plan ops (`owner`/`group`, schema v4) and enforced on apply; owner is a user name, group is a numeric gid or group name (resolved via `os/user`). Only explicitly set ownership is recorded — the build-time default (current user) is not pushed to remote hosts, and absent files carry no ownership. `WithMode` accepts setuid/setgid/sticky: either raw octal (e.g. `0o4755`) or Go flag form (`0o755\|os.ModeSetuid`); both normalize to the flag form, lower to a four-digit plan wire mode (`"04755"`), and land on disk (apply chowns before it chmods so unprivileged chown cannot clear the special bits). Bits above `0o7777` are rejected. Modes without owner-read (e.g. `0o000`) are applied on non-root runs too: the attribute step falls back to path-based `chmod`/`chown` when the descriptor-based open is denied (never through a symlink at the target). |
| `WithFileMode` | Dir | Mode for files created from a source tree (same setuid/setgid/sticky handling as `WithMode`) |
| `WithPrune` | Dir | Remove unexpected children when syncing / absent; the two sync flavors prune differently, see below |
| `WithSymlink` / `WithHardlink` | Link | Link target (Link does not take owner/mode options) |
| `IsAbsent` / `No*` | all | Ensure missing |
| `DependsOn` | all | Apply after other resources |

Helpers that wrap these: [helpers.md](helpers.md) (`InstallFile`, `SyncDir`, `EnsureDir`, `EnsureFile`, `LinkIfExists`, `SymlinkMap`). `EnsureFile` creates an empty regular file only when absent; existing regular files retain their content while explicitly supplied mode, owner, and group converge.

### Keyed lines in shared files

`WithLine` matches whole lines byte for byte, so it cannot take over a
setting whose existing line differs: a quoted legacy
`export PKG_PATH="…"`, an older value, or an administrator's edit would stay
beside the new line as a second, conflicting assignment. `WithKeyedLine(key,
line)` owns the setting instead of the text: the first existing line starting
with `key` (a literal prefix, not a pattern) is replaced **in place** by
`line`, every further line starting with `key` is removed, and `line` is
appended when no line starts with `key`. Every other line, comment and the
file's order are left alone, so it is safe on shared rc/profile/daily files
that are not owned whole. A replaced differing line is logged at Info (key
and counts only, not the old text), and the file change is reported like any
other content change.

This safety is about the *lines only*: like every other `File`, a keyed edit
does **not** preserve a shared file's existing mode or ownership. `build()`
defaults an unset mode to `0640` and applies that default unconditionally —
mode is not gated by whether `WithMode` was called (see the
`WithOwner`/`WithGroup`/`WithMode` row above for the ownership default's own,
narrower rule) — so a root-owned `/root/.profile` at `0644` silently becomes
`0640` under a bare `WithKeyedLine`, the same trap `WithLine` and
`WithContent` already have. Pass `WithMode`/`WithOwner`/`WithGroup` explicitly
(as in the example above) whenever the file's existing mode or ownership must
survive.

**Pick `key` as narrow as the setting itself** (e.g. `"export PKG_PATH="`,
not `"export "`): declaration-time checks (below) validate that `key` and
`line` are shaped correctly, not that `key` is specific enough for a given
file's actual content. A key that is accidentally too broad matches, and
therefore drops, every other line that happens to share the prefix — for
example `"export "` also matching unrelated `EDITOR`/`PAGER`/`HTTP_PROXY`
lines and silently deleting them. A drop (as opposed to a plain replace) is
therefore logged at Warn instead of Info, so it stays visible even under
`-quiet` (which lowers the level to Warn, so Info and below are suppressed);
the dropped lines' text is never logged above Debug, since it is not
necessarily redaction-safe.

Rules, checked when the resource is declared (misuse fails fast):

- `line` must start with `key` and must not contain a line break;
- within one File, a key is declared once (an exact repeat is ignored), and
  no key may be a prefix of another (`"A"` and `"AB="` would both own
  `AB=1`) — include the delimiter, e.g. `"export PKG_PATH="`;
- no `WithLine`/`WithoutLine` line may start with a key: the keyed edit
  already owns it;
- like the other line edits it cannot combine with `WithContent`,
  `WithSource`, `WithValidation`, `EnsureFile` or `SecretFile`.

Edits apply in a fixed order: `WithoutLine(s)`, then `WithKeyedLine`, then
`WithLine(s)`. Two separate File declarations of one path that key the same
setting differently are not detected and would fight; keep one owner per
setting. On the wire the edit is a file op's `keyed_lines` (plan schema 23).
Only a plan containing a keyed edit declares v23, so older destinations
refuse it at the header gate instead of silently ignoring the edit, while
plans without one keep their earlier header.

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

The validator runs without a shell, with stdin from `/dev/null`, in gonf's own
process group (so a terminal's Ctrl-C, hangup or Ctrl-Z, and a `SIGKILL` of the
whole group, reach it as they reach gonf), and is bounded by the same
per-command timeout as every other backend command (`-cmd-timeout` /
`api.SetCommandTimeout`, 5 minutes by default). When the timeout expires, the
validator is killed (`SIGKILL`) together with the processes it started, so a
wrapper script dies with the hung checker it runs, and validation fails with
`... failed: timed out after 5m0s: context deadline exceeded`. gonf first stops
the validator (`SIGSTOP`), then finds and stops its descendants by walking the
process tree (`/proc` on Linux, `ps` elsewhere), spending at most 2 seconds on
the search, and finally kills them children first and the validator last.
Processes are identified by pid and start time, so a pid reused by another
process is not mistaken for one already seen (to the start time's resolution).
Right before the kill, gonf reads the process table once more (spending at most
1 second) and skips every stopped descendant that is gone or whose start time
changed: a process gonf could not stop, such as an ancestor it may not signal,
can resume a stopped child and let it exit, and its pid could then name an
unrelated process. The check is exact to the clock tick with `/proc` on Linux
and to the second where `ps` supplies the start time; if that read fails, the
stopped descendants are killed anyway. A process that was already detached from
the validator (e.g. by a double fork), not found within those 2 seconds, or
that could not be stopped (it exited meanwhile, so its pid may be reused, or
gonf may not signal it) is not killed; if the process table cannot be read,
only the validator itself is. Processes started by a validator that exits on
its own before the timeout keep running. If gonf is not allowed to kill the
validator (`EPERM`, e.g. a non-root gonf running the validator through
`sudo`/`doas`), the timeout cannot bound it and gonf waits until it exits; 2
seconds after the failed kill gonf also stops reading its output, so its next
write may kill it with `SIGPIPE`, reported as `... failed: signal: broken
pipe`. If a process it started still holds the validator's stdout/stderr, gonf
stops reading that output 2 seconds after the validator exited or was killed
and continues without waiting for it (a validator that exited 0 still counts as
a success). The live file stays untouched and the candidate is removed on a
timeout too. A failure includes the validator's combined stdout/stderr, with
lines joined by ` | ` and control characters and invalid UTF-8 replaced by `?`.
All output is read, but only the first 4 KiB are kept and the rendered text is
cut to at most 4 KiB; when anything was cut, a note such as `[output truncated,
1000000 bytes in total]` follows (or `[no printable output; N bytes in total]`
when what was kept is only whitespace):

```
file /etc/httpd.conf: validation by httpd failed: exit status 1: validator output: /etc/httpd.conf.gonfvalidate123:12: syntax error
```

Gonf never adds candidate content to the error, but whatever the validator
prints is reported (and logged), so do not use a validator that echoes secret
input. Output of a successful validator is discarded.

This is deliberately a **single-file** contract. The candidate shares the live
file's parent directory, which preserves ordinary relative-path resolution, but
that directory must be owned by the applying uid, not world-writable, and
group-writable only when its group is your user-private group (your effective
gid, equal to your uid, as with the `0775` directories that the umask-002 default of Fedora
and most Linux distributions creates; gid `0` is never private, so a root apply
gets no such exception; the same rule as for `gonf plan -o` directories).
Every ancestor must likewise be owned by root or the applying uid; ancestors
that are world-writable, or group-writable by a group other than your private
group, also need sticky protection (as with `/tmp`). Symlinks and `..` path
components are refused. Otherwise another
user could replace the
candidate after it is closed and before the validator opens it; Gonf refuses
such a path rather than treating a `0600` candidate as sufficient protection.
The path is checked by opening each directory without following symlinks and judging the
opened directory itself; on Linux and FreeBSD this needs only search (`x`)
permission on each ancestor, as before, while on macOS, NetBSD and OpenBSD
every ancestor must also be readable by the applying user (a non-root apply
below another user's `0711` directory fails there with `permission denied`;
root is not affected).
It cannot safely validate a configuration whose includes, chroot, or companion
files must move together. Use a future multi-file configuration resource for
those cases.
Concurrent validations use separate candidates, but concurrent publishes to
the same target are not serialized: each is atomic individually and the last
successful rename wins. Several files are likewise not an atomic transaction;
a failure after one publication requires a later apply or explicit recovery to
converge the set.

### Pruning a synced directory

`WithPrune` reconciles the destination against the sync source, and what
counts as "unexpected" follows the flavor of the sync:

- **Tree sync** (`WithSource(dir)`): every destination entry without a
  counterpart in the source tree is removed, subdirectories (recursively),
  symlinks and other non-regular entries included. A destination entry also
  counts as expected when the source holds the same relative path with a
  `.tmpl` suffix, since that renders to the stripped name.
- **Glob sync** (`WithSourceGlob(pattern)`, and therefore `SyncDir`): only
  regular files directly under the destination are removed, and only when
  their name is not the name a counting match installs (Rex `prune_dir`
  semantics). Subdirectories, symlinks and other non-regular destination
  entries are left alone, and nothing below a subdirectory is touched.

Both flavors behave identically on the direct path and through a plan: the
`sync_dir` op records the glob flavor as `glob` (schema v24) so the
destination rebuilds the same sync from the flattened blob. Before that fix a
glob sync applied through `gonf apply` / `push` pruned with tree semantics and
deleted unmanaged subdirectories of the destination.

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
