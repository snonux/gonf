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
| `WithBlock(name, lines...)` | File | Own the marked region between `# BEGIN GONF <name>` and `# END GONF <name>` of a shared file; see below |
| `WithName` | File / Command | Explicit resource identity. A named File keeps managing its supplied path but is registered and reported as `File[name]`, allowing separate line edits to one file and precise `DependsOn` / `OnChange` wiring. Without it, file IDs remain `File[path]` and duplicate registrations still fail. |
| `WithOwner` / `WithGroup` / `WithMode` | File / Dir | Ownership and mode. Recorded in plan ops (`owner`/`group`, schema v4) and enforced on apply; owner is a user name, group is a numeric gid or group name (resolved via `os/user`). Only explicitly set ownership is recorded — the build-time default (current user) is not pushed to remote hosts, and absent files carry no ownership. `WithMode` accepts setuid/setgid/sticky: either raw octal (e.g. `0o4755`) or Go flag form (`0o755\|os.ModeSetuid`); both normalize to the flag form, lower to a four-digit plan wire mode (`"04755"`), and land on disk (apply chowns before it chmods so unprivileged chown cannot clear the special bits). Bits above `0o7777` are rejected. Modes without owner-read (e.g. `0o000`) are applied on non-root runs too: the attribute step falls back to path-based `chmod`/`chown` when the descriptor-based open is denied (never through a symlink at the target). |
| `WithFileMode` | Dir | Mode for files created from a source tree (same setuid/setgid/sticky handling as `WithMode`) |
| `WithPrune` | Dir | Remove unexpected children when syncing / absent; the two sync flavors prune differently, see below |
| `WithSymlink` / `WithHardlink` | Link | Link target (Link does not take owner/mode options) |
| `IsAbsent` / `No*` | all | Ensure missing |
| `DependsOn` | all | Apply after other resources |

Helpers that wrap these: [helpers.md](helpers.md) (`InstallFile`, `SyncDir`, `EnsureDir`, `EnsureFile`, `LinkIfExists`, `SymlinkMap`). `EnsureFile` creates an empty regular file only when absent; existing regular files retain their content while explicitly supplied mode, owner, and group converge.

### Managed blocks in shared files

`WithBlock(name, lines...)` is for a file several writers share by region
rather than by setting: /etc/hosts with fleet rows from gonf and VM rows
from a provisioning tool is the motivating case. gonf owns exactly the lines
between `# BEGIN GONF <name>` and `# END GONF <name>` (the same marker style
as the Cron resource's crontab entries) and never reads or rewrites a line
outside them.

- The block is applied before `WithoutLines`, keyed lines and `WithLines`,
  and declaration refuses any of those that would act on a block or marker
  line, so a stale line left in an old block region is replaced before
  another edit could see it and one pass converges.
- A file without the markers gets the block appended; a missing file is
  created with it. Anything else than one BEGIN followed by one END is
  refused without writing: guessing the region could delete another
  writer's lines.
- Markers match after trimming surrounding whitespace and a matched marker
  line is kept verbatim, so an indented marker does not churn.
- Wire: the file op's `blocks` field (`[{"name":..,"lines":[..]}]`), plan
  schema 27, declared on demand like `keyed_lines` (v23).

### Keyed lines in shared files

`WithLine` matches whole lines byte for byte, so it cannot take over a
setting whose existing line differs: a quoted legacy
`export PKG_PATH="…"`, an older value, or an administrator's edit would stay
beside the new line as a second, conflicting assignment. `WithKeyedLine(key,
line)` owns the setting instead of the text: the first existing line starting
with `key` (a literal prefix, not a pattern, but matched after stripping the
line's own leading spaces/tabs — see "What counts as a match" below) is
replaced **in place** by `line`, every further line starting with `key` is
removed, and `line` is appended when no line starts with `key`. Every other
line, comment and the file's order are left alone, so it is safe on shared
rc/profile/daily files that are not owned whole. A replaced differing line is
logged at Info (key and counts only, not the old text), and the file change
is reported like any other content change.

The file's line endings are normalized as a side effect of any line edit
(`WithLine(s)`/`WithoutLine(s)`/`WithKeyedLine`, not just a keyed one): gonf
writes the result back using the file's own *dominant* terminator — `\r\n`
when the file has strictly more CRLF line endings than bare LF ones, else
`\n` — rather than preserving each line's original terminator individually.
A homogeneously CRLF-terminated file (a Windows-authored `.profile` mirrored
onto a Unix host, say) therefore keeps its CRLF endings across a keyed edit;
a file with a small minority of stray CRLF or LF lines has that minority
normalized to match the majority. A brand-new file created by the edit (the
key or line was missing and the file itself didn't exist yet) gets plain
`\n`.

**What counts as a match.** The key match tolerates the matched line's own
leading whitespace: `"  export PKG_PATH=old"` (leading spaces) and a
leading-tab-indented line are both recognized as owned by
`WithKeyedLine("export PKG_PATH=", ...)`, and the replacement is written back
*unindented* — the key now owns the line's position, not its original
indentation. The match does **not** tolerate anything past the leading
whitespace: extra internal whitespace (`"export  PKG_PATH=old"`, two spaces)
and case differences (`"EXPORT PKG_PATH=old"`) are left untouched, and the
new line is appended beside them as a second, conflicting assignment —
exactly the duplicate this feature otherwise exists to prevent. Safely
normalizing internal whitespace or case would need real parsing (word
splitting, and case-folding a key that may itself be case-sensitive shell
syntax) this feature does not attempt; pick a key whose known variants are
covered, or accept that a hand-edited line with unusual internal spacing or
case needs its own separate `WithKeyedLine` (or a one-off `WithLine`/
`WithoutLine` pair) to converge.

Because the match strips the *candidate line's* leading whitespace before
comparing it against `key`, `key` itself must not start with a space or tab:
an untrimmed, whitespace-leading key could never equal that trimmed prefix,
so no line would ever be recognized as owned and `line` would be appended as
a brand-new line on every single apply — unbounded duplicate-line growth,
with nothing logged (a plain append is neither a replace nor a drop). This is
refused at declaration time; write the key without its indentation (e.g.
`WithKeyedLine("ServerName ", "ServerName foo")` to own an indented
`    ServerName foo` line in an nginx block) — `line` must itself start with
`key` (the rule above), so it too carries no leading whitespace, and the
owned line is written back unindented regardless, per "What counts as a
match" above.

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
- `key` must not start with whitespace (see "What counts as a match" above);
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

### Controller-side rendering (`api.RenderTemplate`)

`api.RenderTemplate(path string, data any) (string, error)` reads and renders
a template file on the *controller*, before any destination or `File`
resource exists, for recipes that must resolve content from typed, explicit
data structs — controller-only topology inputs such as an ACME host list or
a cluster's member list — and then hand the fully-rendered text to
`WithContent`, instead of shipping a template plus data for
`WithTemplateData` to render on the destination (the path documented above).
It reuses the exact template engine the destination path uses: the same
helper functions (`join`, `lower`, `upper`, `trim`, `replace`) and the same
strict `missingkey=error` handling. `data` must be JSON-compatible the same
way `WithTemplateData`'s data must be: it is JSON-encoded once, a top-level
JSON object's keys become root template variables, and the complete decoded
value is also available as `.Data`.

**What's absent at the controller site, and why it fails loudly rather than
silently.** Unlike the destination render (`File.applyTemplateToContent`):

- No `.Gonf` (`.Gonf.GOOS`, `.Gonf.Profile`, `.Gonf.Hostname`): there is no
  destination yet when a controller render runs, so there are no live
  destination facts to supply.
- No process environment: the destination render mixes `os.Environ()` into
  the template data; the controller render does not.
- No `.Param`: the destination render's `{{.Param}}` is the recipe's
  declared source identity (see "Template `{{.Param}}` in synced trees"
  above); nothing analogous exists before a destination is chosen.

Because templates use `missingkey=error`, a reference to any of these three
does not render as an empty string — it fails the render (and so fails plan
recording) with a missing-key error. This means a template **targets one
site, controller or destination, and not both interchangeably**: a `.tmpl`
asset written for the destination site (referencing `.Gonf`, environment
variables, or `.Param`) hard-fails when rendered through
`api.RenderTemplate`, even though the same file works fine as a
`WithSource`/`WithTemplateData` destination template.

**On the plan wire.** The controller-rendered text is not itself carried as
a template: the caller hands the already-resolved string to `WithContent`,
so it becomes a plain `content_b64` in the recorded plan op — the same as
any other literal `WithContent` string, with `template` left `false` and no
`template_param`. Contrast "Template rendering for a single `File` on the
plan path" below, where the source stays a real, still-`.tmpl`-suffixed
template through recording and is rendered again on the destination at
apply time. A controller-rendered value is therefore baked into the plan at
record time and is never re-rendered.

**Secret-withholding in render errors.** A failed `api.RenderTemplate` call
redacts every value `ResolveSecret`/`MustSecret` has returned in this
process (`api.RedactSecrets`, which replaces known secret values by text,
not by withholding the whole message) out of the returned error before
handing it to the recipe: the underlying `text/template` parse/execute
error can otherwise quote the offending value verbatim, and unlike a
destination render there is no `File` resource here to carry
`WithSensitive`/check `Sensitive` and withhold the whole message instead
(see `templateError` in `resource/file/template.go`). The tradeoff this
leaves: a non-sensitive template's render error is unaffected and fully
useful for debugging, while a secret-bearing one has that value scrubbed
from its error text — the failure is reported without leaking the secret,
but also without showing it, so diagnosing exactly what was wrong with a
secret's shape from the error alone is not possible.

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

See also [examples/examples.go](../../examples/examples.go).
