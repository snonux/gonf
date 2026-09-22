# Plan / apply (local and remote)

gonf has **one** mutation engine: a versioned JSONL **plan** of resource ops,
interpreted by `plan.Apply`. Local and remote both use it.

```text
gonf <task>…              RecordPlan → Apply          (local one-shot)
gonf plan -o dir …        RecordPlan → write plan.jsonl (+ blobs/)
gonf apply plan.jsonl     DecodePlan → Apply          (any host with gonf)
gonf apply -              DecodePush from stdin       (JSONL or GONF-PUSH/1)
gonf push [-n] [-id] [-- ssh…] user@host <task>…  # stream over ssh
gonf fleet [-n] [-j N] <fleet> <task>…            # parallel push to inventory
gonf hosts | fleets                               # list inventory
```

Tasks marked `Privileged()` are applied via a separate `sudo -n` / `doas` `gonf apply`
invocation (see Host `WithPrivilege`). Unprivileged tasks use plain `gonf apply`.

### Where dependencies are checked

A `DependsOn` naming an ID no recorded op carries (a typo, or a resource that
was never registered) is a *dangling dependency*. The plan applier itself
cannot detect it: `gonf apply <plan.jsonl|->` and the elevated re-exec child
execute **single privilege chunks**, whose deps legitimately live in an earlier
chunk, so a dep missing from the body must count as satisfied there. The same
is true of every chunk-level entry point: `api.ApplyPlan`, `api.PushPayload`
and `api.PushPayloadContext` (an already-encoded payload plus an elevate flag)
run no pre-flight at all. The check
(`plan.ValidateChunks`, built on `plan.ValidateChunkDeps`) therefore runs
wherever the **whole plan** is in hand, before anything is applied or uploaded:

| Where | Covers |
|-------|--------|
| record time (`api.RecordPlanTo`) | `gonf <task>`, `gonf plan`, push, cluster, fleet — dangling and forward cross-chunk deps, and cross-chunk change watches; the refused plan is never shipped or applied, and `gonf plan -o dir` writes nothing (see below) |
| `api.ApplyChunks` | local apply of an already-recorded plan (same checks) |
| `remote.Delivery.ToHost` | every SSH push and strict preview (single target, cluster, fleet), before any SSH traffic (same checks) |
| `api.Apply` | registered resources, split into privilege chunks like `Run` after a dependency sort (see "Low-level `Apply()`"); without elevated ops the plan is one chunk, so only dangling deps and watches can fail; with them a dependency cycle and a watch crossing the privilege boundary are refused too |

All four run the one helper `plan.ValidateChunks` (dependency direction, then
change-gate locality), so the checks cannot drift apart. `Run` therefore
validates twice on purpose: at record time, and again in `ApplyChunks`, which
is a public entry point that cannot assume its ops came from a record in this
process (they may be decoded from a file or written by an older gonf). Both
passes run the same function on the same plan, so the second never disagrees.

`RecordPlan` (and so `gonf plan -o dir`) records into a private staging
directory and copies the blobs into `dir` only after the whole record,
pre-flight included, succeeded. Blob names are deterministic, so packaging
straight into `dir` would let a refused run overwrite the blobs of an earlier
good plan in the same directory and change what that plan's `plan.jsonl`
applies. What is guaranteed, and what is not:

- After any error while recording or validating (a refusal, a task-body,
  cycle or packaging error) `dir` is exactly as it was (byte for byte and
  mtime for mtime), and a `dir` that did not exist is not created. The same
  holds when `dir` itself is unusable (a symlink, or below a symlinked
  directory; a file; owned by another user; world-writable or group-writable
  by a group other than your private group; not writable by you; nowhere
  writable to create it): a cheap best-effort check rejects those before any
  task body runs, creating and changing nothing. It is a pre-check, not a
  guarantee, because the path can change before the commit; so the commit
  re-checks `dir` before the first blob is written: the same directory rule
  (type, no symlinked component, owner, group/world write) through a
  no-follow open, plus the "writable by you" check, which is not part of that
  rule and is asked of the directory the no-follow open verified (through its
  descriptor), not of whatever the path names by then. Either refusal writes
  nothing. The commit keeps the descriptor of the
  `dir` it verified and writes every blob relative to it, never through the
  path again: it opens `blobs/` below that descriptor without following a
  symlink, checks it against the rule, and writes single-file and tree/glob
  blobs through the verified `blobs/` descriptor, so swapping `dir` or
  `blobs/` for a symlink after the check cannot redirect a blob.
  `plan.jsonl` re-checks `dir` itself through a no-follow walk of its whole
  path (see "The output directory"). Errors during those writes
  (for example a leftover `blobs/` or blob tree you cannot write or replace,
  a full disk, or a change after the re-check) are plain I/O errors with the
  non-atomic caveat of the next point. `-o ''` is treated as `-o .`.
- An I/O error while COMMITTING the blobs into `dir` after a successful record
  (full disk, permissions, a blob path that cannot be replaced) is reported but
  not atomic: some blobs may already be copied, so a partially updated blob
  store is possible.
- The staging directory lives in `$TMPDIR` (owner-only) and is created lazily,
  on the first blob write, so a plan without blobs never touches `$TMPDIR`.
  It is removed on every return path; DSL misuse in a task body is a returned
  record error (a declaration error, see "Error handling contract"), never a
  process exit. It is NOT removed when the process dies without running Go code (SIGKILL, a
  crash) or is interrupted by a signal nothing handles: the staging directory,
  with copies of the packaged sources, then stays in `$TMPDIR`.
- `gonf plan -stdout` records in memory and stages nothing.

`RecordPlanTo`, which writes into a caller-supplied store, gives no such
guarantee: it is for storage the caller discards (`Run`'s temp dir, push's
in-memory store).

A pre-flight refusal reads `RecordPlan: <reason>` with a single prefix — the
`plan:` engine prefix is stripped, and dangling IDs are described in terms of
registered resources — and callers add their own context in front
(`plan: RecordPlan: ...` at `gonf plan`, `record: RecordPlan: ...` at push,
cluster and fleet). That single prefix is true of refusals (and of the few
packaging and plan-dir errors that carry it) only: an unknown task prints
`plan: unknown task "..."`, and cycle and task-body errors have no
`RecordPlan:` prefix at all. A resource kind's own record-time rejection
(its handler's `ToOp` error) is wrapped as
`RecordPlan: task "<task>": draft "<ID>": <reason>`, or without the task
part from `api.Apply`, which records no task; handlers therefore never add
their own ID prefix. The typed errors (`*plan.DanglingDepError`,
`*plan.DanglingWatchError`, `plan.Refusal`) stay reachable through
`errors.As`, also through `api.Apply`.

**Behaviour change:** `gonf plan` now also refuses, at record time, a
dependency on a resource recorded in a LATER privilege chunk (e.g. a
non-elevated op `DependsOn` an op registered after it with `WithElevate`).
Earlier versions recorded such a plan; `Run`, push, cluster and fleet already
refused it (privilege chunks apply in recorded order and are never reordered).
The one path that could still consume it was `gonf apply plan.jsonl`, which
applies a whole plan file as a single chunk and so sorted the dep. To keep such
a recipe, register the dependency before its dependent, or give both the same
privilege.

## Why

Tasks are Go code. Shipping whole Go task trees to every host is awkward.
Instead the controller **records** task bodies into a portable plan; the
destination only needs a gonf binary that understands the plan schema.

Local `Run` / `gonf <task>` uses the same path automatically (temp dir for
blobs, then apply, then cleanup) so local and remote cannot diverge.

## Error handling contract

gonf separates **registration-time** misuse from **runtime** failures. No
library code ends the process: `internal/logger` has no `Fatal`, and gonf
calls neither `os.Exit` nor `panic` for recipe or input errors. Only the
binary's `main` exits, with the code `cli.CLI` returns.

- **Registration-time DSL misuse is a declaration error**
  (`internal/declerr`). The DSL constructors return resources and handles,
  not errors, so misuse cannot be handed back to the recipe line; it is
  reported instead, and the constructor returns an inert value (an
  unregistered resource value, an empty `Multi`, a zero handle, a nil list)
  so the recipe keeps running and later declarations are still checked.
  Covered: duplicate `Task` / `Alias` / aggregate / `Host` / `Cluster` /
  `Fleet` registration, an empty `Task` name or nil `fn`, an `Alias` with an
  empty name or target or naming itself, an `AggregateTasks` with no members,
  an empty or duplicate member, or a member that is the aggregate itself (by
  name or through an `Alias`, in either registration order), an option
  applied to a resource that does not support it (`options.requires`, e.g.
  `*file.File does not support WithRestart`), invalid option values and
  combinations (`WithMode` with bits outside `0o7777`, `OnChange()` with
  nothing to watch, `WithLine` + `WithContent`, `WithSource` +
  `WithSourceGlob`, `WithSensitive` without `WithName` on a `Command`), a
  duplicate resource ID, a refused `SystemdUnits` / `DaemonReload` merge,
  `SystemdUnits` / `LoginClass` / `SymlinkMap` / `EachKV` misuse, a
  `RegisterMethods` receiver or companion with the wrong shape, an invalid
  `Matching` pattern, `SetSecretProvider` misuse, `MustHost` / `MustCluster` /
  `MustFleet` / `MustHostValue` lookups that fail, and `ForHosts` /
  `ClusterHosts` misuse outside a recording.
  - **First error wins.** Only the first report is kept (a later one is
    usually a consequence of it); it keeps the message `logger.Fatal` used to
    print and carries the recipe line it was declared at
    (`declerr.Location`: the first stack frame outside gonf).
  - **Where it surfaces.** A misuse reported while a plan is recorded (inside
    a task body) fails that record like any stashed task-body error:
    `RecordPlan` / `Run` / push / cluster / fleet return it, nothing is
    applied or pushed, and temporary directories are removed by the normal
    deferred cleanup. A misuse reported outside a recording (top-level
    registration in `main`, resources declared for a direct `api.Apply`) is
    kept for the process: `RecordPlanTo`, `Run`, `api.Apply` and
    `resource.Apply` refuse with it before any task body runs, and `cli.CLI`
    refuses every invocation (`-list` and `-version` included) with it, exit
    status 1, printing the message and `declared at <file:line>`. From the
    CLI a broken recipe therefore still exits non-zero with the same message
    as before; an embedding program gets it as an error.
  - **Apply side.** A resource rebuilt on the destination from a plan op
    (the `Ensure*` helpers the plan handlers use) collects option misuse in
    its `embed.Misuse` and returns it as its apply error, so an apply never
    silently drops an option.
- **Programmer-bug invariants may still panic**, each documented at its
  site: an unreachable `default` of a `Path` (`string | []string`) type
  switch in `api`, a duplicate `plan.RegisterHandler` for one op kind (two
  gonf packages claiming one wire kind at `init`), an op field type the
  secret walker does not classify (`walkOpStrings`, guarded by a fitness
  test), the test-binary exec guard in `internal/remote` and misuse of the
  test-only `internal/testutil` helpers. None of them is
  reachable from a recipe or from input data.
- **Record-time failures return errors**: unknown tasks, recursion cycles,
  packaging failures, registered resources without plan drafts, dangling or
  cross-privilege-chunk `DependsOn` / `OnChange` targets, an `Alias` with an
  unknown target or naming another alias, an unknown `AggregateTasks`
  member, a failing child task inside an `Aggregate` / `AggregateTasks`, and
  a failing nested `Run` inside any task body all fail `RecordPlan` / `Run`
  with a returned error. Task bodies cannot return errors, so aggregates
  stash the failure (`stashAggregateError` in `api/plan.go`, same mechanism
  as the cycle stash) and the enclosing record fails with `aggregate <name>:
  <cause>`; a nested `Run` stashes `task <body>: <cause>` even when the body
  ignores the error it returned.
  Nothing is applied in that case: the abort happens during recording, before
  plan apply runs.
- **Secret failures return record-time errors**: `MustSecret` and
  `OptionalSecret` resolve secrets through the configured provider (by default
  controller-local `secrets/<path>` files) while a task is recorded. Required
  missing secrets, empty secrets, unreadable files, unsafe paths and an
  unavailable provider (e.g. no `secrets/` directory at all) fail before local
  apply or SSH push; only a secret the provider reports as not found
  (`secret.ErrNotFound`) lets an optional lookup omit that host's fragment.
  This preserves deferred plan-directory cleanup and prevents a partial plan
  from reaching a host. See [secrets.md](secrets.md).
- **Apply-time failures return errors**: `plan.Apply`, the resource `Ensure`
  helpers, and `ApplyChunks` never exit the process. The error travels up to
  the CLI (or the embedding caller), which prints it and exits 1 — so deferred
  cleanup (temp plan dirs, apply run dirs) always runs, and `gonf` stays
  usable as an embedded library.
- **Push-time refusal returns an error, before any SSH traffic**: `PushTo` /
  `PushClusterRun` / `PushFleetRun` call `RefuseOpaqueOnlyPush` right after
  `RecordPlanTo` returns. If any recorded task's `When` is opaque with no
  serializable guard at all (see "Recording" above), the push is refused —
  naming the task(s) — before the blob upload or any apply chunk goes over
  SSH. There is no opt-in to silence this; the same recording still succeeds
  for local `Run` / `gonf plan`, where the refusal does not apply.

Temporary plan directories — `Run`'s `$TMPDIR/gonf-plan-*`, `Apply`'s
`$TMPDIR/gonf-apply-*` and `RecordPlan`'s staging directory (`gonf plan -o`)
— are removed by deferred calls on every return path, success or error,
because no library code exits the process (a declaration error in a task
body is a returned error). A process killed by a signal nothing handles, by
SIGKILL or by a crash leaves them behind in `$TMPDIR` (owner-only, `0700`).

## Recording (`RecordPlan`)

```go
ops, err := RecordPlan("my-plan", planDir, "home_helix", "home_tmux")
```

- Looks up **candidates** (not Activate-filtered) so serializable `When*`
  become `when_begin` / `when_end` recipes evaluated on the destination.
- Task bodies run with a draft recorder: `File` / `Dir` / `Link` / `Command` /
  … emit ops instead of applying.
- Every draft must map through a registered `plan.Handler` (`api/plan.go`'s
  `draftToOp` looks one up by `d.Kind`): an unknown draft kind fails
  `RecordPlan` loudly at record time (there is no `default:` passthrough to
  the wire), so a typo or a forgotten registration is a controller-side
  record error instead of a remote apply-time `unknown op`.
- `InstallFile` content → `content_b64` when ≤ 512 KiB, else a blob sidecar.
  A legitimately empty file (`WithContent("")` or an empty `WithSource` file)
  also encodes as `content_b64:""`, so the `file` op carries `has_content:true`
  whenever content/source was configured at all; apply treats `content_b64`
  and `blob` both empty **and** `has_content` false as a record-time bug
  ("missing content_b64 and blob"), not as an empty file.
- A `File` whose declared source or path ends in `.tmpl` carries that intent
  onto the `file` op explicitly (`template:true` plus `template_param` =
  the declared source path, schema v9): by record time the op's `path` is
  already stripped of `.tmpl` and its content is packaged as raw bytes into
  `content_b64`/`blob`, so neither field still carries the suffix
  destination apply would otherwise key rendering off. Apply forces
  rendering (`opt.WithTemplate`) and reproduces the direct-run `{{.Param}}`
  (`opt.WithParam(template_param)`) before writing the file. `SyncDir`/`Dir`
  source-tree copies are unaffected — their per-file `WithSource` still
  carries `.tmpl` at apply time (see [file-dir-link.md](file-dir-link.md)).
- `WithTemplateData` is encoded as `template_data` on `file` ops (schema v12).
  It remains JSON data until destination apply, where the file handler exposes
  it to the template along with live destination facts under `.Gonf`.
- `WithValidation` is encoded as `validation_bin` and `validation_args` on a
  content-managed `file` op (schema v18). `validation_args` retains exactly
  one `CandidatePath` wire token; destination apply replaces it with a private
  validated candidate before the live file is published. The op still carries
  `has_content:true`, including for an intentionally empty source/content, so
  apply can reject a malformed validator op that has no declared content.
- `SyncDir` trees → `planDir/blobs/<name>/`. On apply, every `.tmpl` entry
  of the synced tree (tree and glob flavor) renders `.Gonf.GOOS`,
  `.Gonf.Profile`, and `.Gonf.Hostname` from the same destination plan facts
  as a single-file `file` op (detected per `api.ApplyPlan` call, i.e. per
  privilege chunk), so identical template text renders identically in one
  apply. On local applies (`gonf <task>`, `gonf apply`, and their elevated
  re-exec) those facts honour the CLI `-profile` override; on `push` /
  `fleet` the remote `gonf apply` is not passed `-profile`, so the
  destination renders from its own detected facts.
- The recipe's declared source directory travels on the `sync_dir` op
  (`source_dir`, schema v6): destination apply renders `.tmpl` files inside
  the tree with `{{.Param}}` = declared source dir + "/" + the entry's path
  relative to the tree root (for the glob flavor: the declared glob pattern's
  directory + "/" + basename). Without it — direct resource use aside, which
  derives the same value from its real source tree — the Param would be the
  ephemeral blob-extraction path, which changes every plan run and would flap
  the rendered checksums (see [file-dir-link.md](file-dir-link.md)).
- `SyncDir` packaging is one shared function (`plan.scanTree`) for both blob
  stores: directories (empty ones included) and symlinks (raw target string,
  dangling included — never read through) are preserved as themselves,
  regular files by content; FIFOs/sockets/devices fail loudly. Glob packaging
  is the flat regular-file subset: files by content (symlinks to regular
  files read through), everything else skipped — matching the direct
  `WithSourceGlob` semantics, so glob-sourced sync_dir ops behave identically
  local and remote. Local (`plan -o dir` / `Run`) and remote (`push` /
  `fleet`) therefore package trees identically, and the destination
  reproduces the source tree 1:1 — a symlink in a source TREE is a symlink
  at the destination.
- Nested recording — a task body calling `Run`, an `Aggregate` /
  `AggregateTasks` recording its members, an `Alias` recording its target —
  appends into the **same** plan; apply happens once at the top level.
  Within one aggregate tree each task is recorded once (see
  [tasks.md](tasks.md#aggregates) for the exact rule).

### Helpers that emit recipes (not controller Stat)

| API | Plan op |
|-----|---------|
| `WhenLinux` / `WhenProfile` / `WhenHostnameContains` | `when_begin` with fact predicates |
| `WhenPathExists(path, fn)` | `when_begin` with `path_exists` around `fn` |
| `WhenHostname(substr\|List(...), fn)` | `when_begin` with `hostname_contains` around `fn` (`List` → one block per entry) |
| `LoginClass` / `NoLoginClass` | `when_begin` with `goos` and `require` (v20) around the fragment `file` ops |
| `EnsureDir` | `ensure_dir` |
| `EnsureFile` | `ensure_file` |
| `LinkIfExists` / `SymlinkMap` | `link_if_exists` |

`WhenProfile(a)` lowers to `{"fact":"profile","eq":"a"}`. `WhenProfile(a, b,
…)` (more than one profile) lowers to the same predicate shape with an
OR-list instead: `{"fact":"profile","in":["a","b"]}` — the destination
matches if the fact equals *any* entry of `in` (schema v8; `hostname_contains`
supports `in` the same way, as a substring-OR). Multiple profiles are just as
serializable as one; `WhenProfile` never marks a task opaque.

Opaque `When(func(Facts) bool)` cannot be serialized — it has no declarative
equivalent. `RecordPlan` requires opaque predicates to pass on the
controller (evaluated against the controller's own `DetectFacts()`), or it
errors. Critically, an opaque predicate is only ever an *extra*
controller-side filter: it never suppresses a task's serializable guards
(`WhenLinux` / `WhenProfile` / `WhenHostnameContains`), which are always
emitted into the recorded plan when present, alongside or without an opaque
predicate. A task built as `WhenLinux()` + `When(fn)` still ships its `goos`
guard — the opaque `fn` only gates whether recording happens at all.

A task whose `When` is opaque **only** (a bare `When(fn)`, with no
serializable guard alongside it) has nothing to ship: the condition only
ever ran on the controller, so the recorded plan carries no guard for it at
all. `RecordPlan` still succeeds — `Run` / `gonf <task>` applies the result
immediately on the very host that evaluated the predicate, so there is
nothing to lose in transit, and `gonf plan` records the same way (the
caller's responsibility not to hand a plan recorded for this host to `gonf
apply` on a different one). `PushTo` / `PushClusterRun` / `PushFleetRun`
(`gonf push` / `gonf cluster` / `gonf fleet`) — which explicitly ship the
plan to a different destination — refuse instead: the push call returns an
error naming the offending task(s) before any SSH traffic, the same "refuse
before mutation" contract as the privilege and cross-chunk-dep checks below.
There is no opt-in flag to bypass this — replace the opaque predicate with
`WhenLinux` / `WhenProfile` / `WhenHostnameContains`, or move the check out
of `When` and into the task body (e.g. guard a `Command` with
`options.OnlyIf`/`options.Unless`, which evaluates on the destination).

## Applying (`ApplyPlan` / `plan.Apply`)

```go
if err := ApplyPlan(ops, planDir); err != nil { /* … */ }
// ApplyPlan = DetectFacts() + plan.Apply(...)
```

- First line must be `{"op":"plan","version":1,…}`. Unsupported versions are
  refused **before** any mutation.
- Applies resource ops in dependency order: each contiguous run between the
  header and `when_begin` / `when_end` boundaries is topologically sorted by
  the recorded `deps` (stable: among ready ops, recorded order wins), so a
  resource always applies after its `DependsOn` targets. Dep-free plans keep
  recorded order; ops are never reordered across `when_*` boundaries. A dep
  outside the current run is classified by where it is recorded: in this body
  earlier, or in an earlier privilege chunk → satisfied; later in this body
  (a later when-block) → refused before any mutation; nowhere in this body →
  satisfied at chunk level (an earlier chunk or invocation applied it).
  `plan.Apply` itself cannot tell a dep applied by an earlier chunk from a
  typo'd one; the controller-side pre-flight (`plan.ValidateChunks`, run at
  record time and by `ApplyChunks`, `remote.Delivery.ToHost` and
  `api.Apply`, but not when a single chunk is executed — see "Where
  dependencies are checked") refuses dangling deps and forward cross-chunk
  deps before anything is applied. `api.Apply` with elevated ops sorts its
  ops by dependency before splitting them, so for it a forward cross-chunk
  dep can only come from a dependency cycle (a resource depending on itself
  included), which the sort itself refuses first.
- Stackable `when_begin` / `when_end`: failed predicates skip the body
  without touching the filesystem.
- A `when_begin` carrying `require` (v20) is a requirement: a failed
  predicate in an active scope refuses the whole apply (and dry run) before
  any mutation instead of skipping the body. A requirement and every block
  enclosing it may only use host facts (`goos`, `profile`,
  `hostname_contains`); one nested under `path_exists` (or any other
  condition) is refused by the pre-check, before anything is applied.
- Expands `${HOME}` on the destination; unknown `${…}` is a hard error.
- Maps ops to existing resource `Ensure` helpers (`file`, `dir`, `link`,
  `cmd`, `pkg`, …) — same semantics as direct resource APIs.

## Adding a resource kind (checklist)

**Update (task j5):** the "one map per resource, no self-registered codecs"
rule below was revisited — it is exactly the situation the note's own escape
clause anticipated ("revisit only if the kind count makes the checklist
unmanageable"): a recent systemd-timer commit touched 19 files for one new
kind, and two of the three hand-copy sites (`PlanDraft` → `api/plan.go`
`draftToOp` → `plan/apply.go` `applyX`) had already silently dropped a field
twice (`IsLatest`, task m5; `.tmpl` rendering, task g5). `plan/handler.go` now
defines a `Handler` interface (`ToOp` + `Apply`) and a `map[Kind]Handler`
registry (`RegisterHandler`/`HandlerFor`). A resource package can implement
`Handler` for its kind in one file (see `resource/pkg/planwire.go`,
`resource/cron/planwire.go`, `resource/service/planwire.go`) and register it
from an `init()`; `api/packager.go`'s `draftToOp` and `plan/apply.go`'s
`applyActive` both consult the registry first and only fall back to their
explicit switch cases for kinds that have not migrated yet. The handler
registry change itself was a Go-internal ownership/dispatch change; later
schema additions such as file validation still require the documented wire
version bump and compatibility gate.

As of task i5, **every** resource kind uses the `Handler` pattern: `package`,
`cron`, and `service` migrated in task j5; `file`, `dir`, `sync_dir`, `link`,
`link_if_exists`, `ensure_dir`, `command`, `timer`, `daemon_reload`, and
`systemd_timer` migrated in task i5; `user` owns its handler in
`resource/user/planwire.go` (see `resource/{file,dir,link,cmd,timer,
systemd,systemdtimer,user}/planwire.go` — `dir` registers three Handlers for its
three kinds, `link` registers two). `api/packager.go`'s `draftToOp` and
`plan/apply.go`'s `applyActive` are now pure `HandlerFor` dispatch with no
fallback switch cases and no `resource/<kind>` imports of their own — this is
also what lets `plan` be a pure types/codec/split/pushwire/blob-store
package. The packages are layered, not mutually dependent: `plan` imports
only the kind-neutral core (`resource` for `PlanDraft` and the apply report,
`resource/options` for the option constructors behind `OwnerGroupOptions`
and `GuardOptions`), and every `resource/<kind>` backend imports `plan` to
register its `Handler`, so `plan` must never import a `resource/<kind>`
package. `TestPlanImportsOnlyResourceCore` (`plan/layering_test.go`) pins
that rule. Steps 2 and 5
below are always "add a `<kind>/planwire.go` with a `Handler` and register
it"; there is no more legacy switch path for a new kind to fall back to.

A new plan-pushable resource kind touches ~8 places across 6 files. The
fitness tests make every step discoverable: adding a `plan.Kind` without the
record-side mapping or fixtures fails `TestPlanKindFitness` (api),
`TestApplyActiveKnowsEveryResourceKind` (plan), and the AllKinds inventory
test (`plan/types_test.go`). `api/plan_option_fitness_test.go` additionally
proves, per option (not just per kind), that a `plan.Apply` round-trip
produces the same effect as calling the resource's `Ensure` directly — the
class of bug that motivated task j5.

1. **Wire type** — `plan/types.go`: add the `KindX` const and its `allKinds`
   entry (this is the exhaustiveness inventory); add any new `Op` payload
   fields with `omitempty`. If fields change meaning (not just grow), bump
   `CurrentVersion` and add golden fixtures under `plan/testdata/`.
2. **Apply handler** — register a `plan.Handler` for the kind: a
   `<pkg>/planwire.go` implementing `ToOp`/`Apply` (see
   `resource/pkg/planwire.go` for the shape), registered via
   `plan.RegisterHandler` from an `init()`. `Apply` validates required fields
   (e.g. `name`, `path`, `schedule`) before any mutation, then delegates to
   the resource `Ensure`; `plan.OwnerGroupOptions`/`plan.GuardOptions`/
   `plan.ParseMode` are shared wire-decoding helpers worth reusing instead of
   re-deriving `opt.Option`s from `Op` fields by hand. `ToOp` copies every
   slice, map and pointer it takes from the draft (`slices.Clone`,
   `maps.Clone`), so the op never aliases the draft (task 882;
   `TestHandlersToOpDoNotAliasDraft` in api checks every kind). A new
   slice/map/pointer `PlanDraft` field also needs a line in
   `PlanDraft.Clone` (`resource/draft_clone.go`), which
   `TestFullDraftCoversEveryReferenceField` enforces.
3. **Resource draft** — the resource package: set the new draft `Kind` string
   in its `planDraft()` and call `resource.RecordPlanDraft` from `Present`
   (the register-without-draft guard fails the record otherwise). Map absent
   resources onto the same kind with `Absent: true` (see `NoCron`/`NoService`).
   Include `Deps: x.DependsOn.SortedIDs()` so `DependsOn` ordering survives
   the wire.
4. **Draft payload** — `resource/draft.go`: add any new `PlanDraft` fields the
   kind needs (package-neutral, no `plan` import — resource packages must not
   depend on the wire codec). A resource package that owns a `Handler` (step
   2) imports `plan` for `Op`/`Handler`/`RegisterHandler` itself, but
   `resource.PlanDraft` still must not import `plan`, since `plan` itself
   must never import a `resource/<kind>` package back (see the cycle note
   below).
5. **Lowering** — implement `Handler.ToOp`, mapping the draft's fields onto
   the new `plan.Op`. `api/packager.go`'s `draftToOp` only ever calls
   `HandlerFor(d.Kind)`; an unmapped kind errors at record time — there is
   deliberately no silent default and no per-kind case left to add there.
   An error `ToOp` returns comes back wrapped (with `%w`) as
   `RecordPlan: [task "<task>": ]draft "<ID>": <your error>`, so a handler
   must not add its own resource-ID prefix; start the message with what is
   wrong.
6. **Options capability** — `resource/options`: add the task-level knobs
   (`With*` options + the interface the resource implements), following the
   existing small-interface pattern.
7. **Fitness fixtures** — `api/plan_fitness_test.go`: add a `kindFitnessTable`
   entry (draft fixture + round-trip) for the new kind. The apply-side
   dispatch is pinned by `TestApplyActiveKnowsEveryResourceKind`
   (`plan/apply_fitness_test.go`) via `AllKinds()` automatically. Also add a
   case to `api/plan_option_fitness_test.go` per new option the kind gained.
8. **Behaviour tests** — record-side lowering in `api/plan_lower_test.go`
   (kind, payload, IDs stable for `DependsOn`) and apply-side behaviour in
   `plan/apply_test.go` / `plan/e2e_test.go`; update `docs/plan.md` tables
   (recipe table above, schema version notes).

A resource package that registers a `plan.Handler` must never be imported by
the `plan` package itself (that would reintroduce the cycle the registry
exists to break): as of task i5, `plan` imports no `resource/<kind>` package
at all — only the resource-neutral `resource` core package, for
`ResetReport`/`PrintSummary` and the `resource.PlanDraft` type `Handler.ToOp`
takes, and `resource/options`, for the owner/group and guard option
constructors (`plan/layering_test.go` enforces this). A resource package that adds a `planwire.go` also means any
internal-package (`package plan`) test that stubs that resource's runner
directly must move into an external `plan_test` file instead (see
`plan/apply_pkg_test.go`, `plan/apply_systemd_test.go`) — importing the
resource package from a `package plan` test file would be an import cycle
(`plan [plan.test]` → `resource/<kind>` → `plan`), even though the same
import from an external `plan_test` file is fine.

## CLI

| Command | Effect |
|---------|--------|
| `gonf <task> [task…]` | Record + apply locally |
| `gonf plan [-o dir\|-stdout [-with-secrets]\|-redacted] [-id name] <task>…` | Write `dir/plan.jsonl` (+ `blobs/`; `dir` defaults to `.`, is created `0700` when missing, is never chmod'ed when it exists and must be yours, not world-writable and not group-writable except by your private group, see "The output directory" below), or print JSONL to stdout (refused for a plan with `sensitive` ops unless `-with-secrets`), or print a redacted human preview that no gonf applies (`-redacted`); see "Secret material" below |
| `gonf apply [-n\|-dry-run\|-strict-preview] <plan.jsonl\|->` | Apply a plan file, or read **GONF-PUSH/1** / bare JSONL from stdin. The plan file must be a regular file and is not followed if it is a symlink (a FIFO or a symlinked `plan.jsonl` is refused; use `-` for piped input); its directory may be reached through symlinks |
| `gonf push [-n\|-preview] [-id name] [-- ssh-args…] user@host <task>…` | Record in memory, stream over `ssh` to remote `gonf apply -` |
| `gonf cluster [-n\|-preview] [-j N] [-id name] [-host-timeout 10m] <cluster> <task>…` | Resolve inventory cluster; record once; parallel push or strict preview to each host |
| `gonf fleet [-n\|-preview] [-j N] [-id name] [-host-timeout 10m] <fleet> <task>…` | Resolve fleet (list of clusters); push or strict preview on unique hosts |
| `gonf hosts` / `gonf clusters` / `gonf fleets` | List registered inventory |

### The output directory (`-o dir`, default `.`)

`plan.jsonl` and the blobs can hold secret material, and a later
`gonf apply` trusts them, so they are only written into a directory nobody
else can modify. `dir` is nevertheless the operator's directory, not gonf's
(the default `.` is normally the recipe checkout), so gonf never rewrites its
mode:

| `dir` | Result |
|-------|--------|
| missing (any missing parents too) | created, every created component `0700` whatever the umask |
| exists, yours, not writable by others, and group-writable at most by your private group (`0755`, `0750`, `0700`, and the `0775` a fresh checkout gets under umask `002`, ...) | used **as it is**; its mode is not changed. Only `plan.jsonl` (`0600`) and the `blobs/` directory gonf creates (`0700`) are private |
| exists, yours, but read-only for you (`0555`, `0500`, ...) | refused up front, before any task runs, with `cannot write to <dir>: permission denied; run chmod u+w on it, or ...`. **New since m62:** such a directory used to be chmod'ed to `0700` and the run worked; gonf no longer changes the mode of a directory it did not create |
| exists, world-writable (`0777`, `0757`, ...; sticky `/tmp` included) | refused, nothing changed |
| exists, group-writable by a group other than your private group (a shared `2775` project directory, any `g+w` directory of a group that is not yours alone, or any `g+w` directory at all when you are root, whose gid `0` is never a private group) | refused, nothing changed |
| exists, owned by another user (root included: a root run does not take over a user's directory) | refused, nothing changed |
| not a directory, or a symlink (or below one) | refused |

**Why group write is accepted for your private group.** Fedora, Ubuntu, RHEL,
Rocky and most other Linux distributions give every user a *user-private
group* (gid equal to uid, the user its only member) and a default umask of
`002`, so a fresh `git clone` or `mkdir` is `0775`. Nobody but you can write
through such a group, so refusing it protected nothing while it broke the
default `gonf plan -o .` in every recipe checkout. gonf therefore treats `g+w`
as safe exactly when the directory's group is your effective gid, that
gid equals your effective uid, **and** it is not `0` (the standard convention;
the group database is not consulted, so an administrator who added members to
a private group opts out of the protection). Gid `0` is never a private group:
on FreeBSD, macOS and the other BSDs it is `wheel`, whose members could replace
`plan.jsonl`, so a run as root refuses every group-writable directory. World-writable is refused always, sticky bit or not,
and `g+w` on any other group (a supplementary group, a shared project group)
is refused because its members could replace `plan.jsonl`, which a later
`gonf apply` trusts.

A refusal names the directory and the reason (world-writable, or
group-writable by a named group that is not your private group) and says what
to do: `chmod go-w` it, or pass `-o <private dir>`. For a sticky directory such
as `/tmp` (which a root run would otherwise "own") the advice is only to pass
`-o <private dir>`: its mode is not yours to change. Pre-existing parents of
`dir` are only walked (a symlink among them is refused, and reported as a
symlink), not checked.

The same rule applies to the `blobs/` directory inside `dir`, and to that
directory alone: kept as it is when it passes, created `0700` when missing,
refused otherwise (a symlinked `blobs/` is refused too, and nothing is written
through it; a `plan.jsonl` that is a symlink is replaced by the new file, never
written through). That check is not part of the up-front pre-check: a plan that
packages no blob never touches `blobs/`, so an unsafe leftover `blobs/` is
refused only when the blobs are committed, after the task bodies ran but before
anything is written. What is guaranteed:

- `dir` is verified in full (no symlink anywhere in its path) once, and the
  descriptor of exactly that directory is kept for the whole commit. `blobs/`
  is opened below it without following a symlink and verified; every blob is
  then written relative to the verified `blobs/` descriptor: a single-file
  blob (larger than the inline limit) as a `0600` file by atomic rename, a
  tree or glob blob (`SyncDir`, `WithSourceGlob`) by removing the old tree
  and creating the new one (directories `0700`, files `0600`, symlinks with
  their raw target) without following any symlink on the way. Swapping `dir`,
  `blobs/` or anything below it for a symlink after the check therefore
  cannot redirect a blob into the link's target; a leftover symlink in place
  of a blob tree (or inside one) is removed as the link itself, never
  descended into. A tree directory that reappears between the removal of the
  old tree and the creation of the new one (someone with write access to
  `blobs/`, i.e. you, recreated it) is refused, not filled: the commit fails
  with "reappeared after it was cleared" and leaves that directory as it is.
  A failure to clear an old tree names the path below `blobs/` and its real
  cause (for example permission denied on a subdirectory you cannot list).
- The staging directory and the temporary plan directories of `Run` and
  `Apply` are private directories gonf makes in `$TMPDIR` itself. They are
  reached following symlinks, so a `$TMPDIR` behind a symlink (macOS's
  `/var/folders`, a symlinked home) works; within them `blobs/` gets the same
  no-follow check and descriptor-relative writes.
- `plan.jsonl` is written after the blobs, by a separate no-follow walk of the
  whole path of `dir` (it re-verifies `dir`, but through the path: if `dir`
  was swapped for another directory of yours that passes the rule between the
  blob commit and that write, `plan.jsonl` lands in the new one).

This guards against an unsafe or pre-planted `blobs/` and against `dir` or
`blobs/` being replaced by a symlink while gonf writes. It does not guard
against another account that can write `dir` itself (the directory rule
refuses such a `dir` up front) or against your own processes changing it
concurrently. **Behaviour change:**
earlier versions chmod'ed `dir` to `0700` on every run (breaking a served or
shared directory, and changing the mode of the checkout for the default
`-o .`); an unsafe directory that used to be silently narrowed is now refused.

### Inventory DSL (`Host` / `Cluster` / `Fleet`)

```go
blowfish := Host("blowfish",
    WithSSHUser("rex"), WithSSHHost("blowfish.buetow.org"), WithSSHPort(2))
fishfinger := Host("fishfinger",
    WithSSHUser("rex"), WithSSHHost("fishfinger.buetow.org"), WithSSHPort(2))
frontends := Cluster("frontends", blowfish, fishfinger) // default parallelism 5

// Later / other packages:
_ = PushHost(MustHost("blowfish"), "id")
_ = PushCluster("frontends", "base", "commons")

// Optional: Fleet is a named list of Clusters (hosts deduped on push).
Fleet("homelab", frontends /*, other clusters… */)
_ = PushFleet("homelab", "base")
```

`Host` / `Cluster` / `Fleet` auto-register. Look up with `LookupHost` /
`MustHost` / `LookupCluster` / `MustCluster` / `LookupFleet` / `MustFleet`.
`Cluster` takes **`HostRef` handles** (not name strings); a host may appear
**at most once** per cluster. Parallelism: `.Parallel(n)` on the cluster
handle (`n < 1` → all hosts at once). `Fleet` takes **`ClusterRef` handles**.

### Privilege (Task mark + Host helper)

| Knob | API | Meaning |
|------|-----|---------|
| Whether root is needed | `Task(..., Privileged())` | Ops from that task get `elevate:true` |
| How to get root | `Host(..., WithPrivilege(PrivilegeDoas\|Sudo))` or `-privilege=sudo\|doas` | Wrap privileged apply as `doas gonf apply` / `sudo -n gonf apply` |

Default tasks are unprivileged. No auto-inference from `Package` vs `File`.
On REMOTE pushes, `-privilege=none` cannot elevate at all: elevated chunks
error out (the controller cannot know the remote login's privilege) — use
sudo/doas, or drop `Privileged()` when the SSH login is already root. Local
apply keeps the root-controller in-process path.
`options.WithElevate` on a `Command` elevates a single op inside an unprivileged task.

**Remote push (`push` / `fleet`):** `-privilege=none` combined with a
`Privileged()` task (or `options.WithElevate`) is an **error** — the remote
login's privilege is not knowable from the controller, and it must not depend
on whether gonf itself runs as root. Set `-privilege=sudo|doas`, or drop
`Privileged()` from the recipe when the SSH login is already root. Local
apply is unaffected: as root, `-privilege=none` applies elevated chunks
in-process unwrapped.

A `Run` / `push` / `fleet` that mixes both kinds **splits** the plan into ordered
chunks and runs one `gonf apply` per chunk (plain vs wrapped).

```go
Task("home_tmux", "", func() { /* … */ })
Task("pkg_openbsd", "", func() { Package("git") }, Privileged())
Host("blowfish", WithSSHUser("rex"), WithSSHHost("blowfish.buetow.org"),
    WithSSHPort(2), WithPrivilege(PrivilegeDoas))
```

### Remote push (no local disk spill)

`push` / `fleet` record into an in-memory blob store, then encode **GONF-PUSH/1**:

1. Optional gzip+tar of blobs (dirs `0700`, files `0600`, symlinks as
   `tar.TypeSymlink` headers carrying the raw link target — the same
   manifest the local planDir blob tree materializes; empty dirs included)
2. Gzip of the plan JSONL

Remote `apply -` stages under `$TMPDIR/gonf-apply/<uid>/`, sweeps stale dirs on
startup, applies, then wipes the run dir. Inline content threshold is **512 KiB**
(`plan.MaxInlineContent`); larger files become blobs in the push stream.

`PushCluster` records and encodes **once**, then fans the same bytes out over SSH
in parallel (errgroup limit from the cluster or `-j`). The fan-out runs under a
context: a failing host **cancels its in-flight siblings** (their ssh
processes are killed, and they are reported as aborted, not as independent
failures), and `gonf fleet` / `gonf cluster` thread their signal-derived
context so SIGINT/SIGTERM abort the whole push. See [Timeouts and
Cancellation](#timeouts-and-cancellation).

### Fleet parallelism semantics

`Fleet` is a named list of `Cluster`s; `PushFleet` / `PushFleetRun` push to
every **unique** host across the fleet's member clusters (a host that
belongs to two member clusters is pushed once, not twice). The question
this section answers: when a fleet push reaches a host, at what concurrency
does it push to that host's cluster-mates?

**Decision: each member cluster's own `.Parallel(n)` applies, not a
fleet-wide default.** `PushFleetRun` groups the fleet's deduplicated hosts
by the cluster that first claims them, and pushes each group at *that
cluster's* configured parallelism (falling back to
`defaultClusterParallelism` only if the cluster itself never called
`.Parallel(n)` — exactly like a direct `gonf cluster` push). Different
clusters' groups run concurrently with each other; only the fan-out *within*
one cluster's hosts is bounded by that cluster's own limit. `-j` (the CLI
flag / `parallelOverride` parameter) is an explicit per-run override and, when
given, applies uniformly to every group, winning over both the per-cluster
setting and the default.

This matters because `.Parallel(n)` exists to protect specific hosts (a
cluster of fragile or rate-limited hosts might set `Parallel(2)` on purpose).
Before this was fixed, `PushFleetRun` ignored every member cluster's
`.Parallel(n)` entirely and always fanned the whole fleet out at
`defaultClusterParallelism` (5) — so a cluster explicitly throttled to
`Parallel(2)` via `gonf cluster` would silently get hit with up to 5x the
concurrency the moment it was reached via `gonf fleet` instead. See
`api/cluster_test.go`'s `TestPushFleetHonorsClusterParallel` for the
regression test (it fails against the old flat-default behavior).

Parallelism is per-cluster, but failure cancellation is still whole-fleet,
exactly as it was before this section's fix. Each member cluster's hosts are
pushed via their own `remote.Fanout` call (so a failing host still aborts its
own cluster's in-flight siblings first, same as always), but `PushFleetRun`
also creates one shared `context.WithCancel(ctx)` up front and passes the
derived context into every group's push call; the instant *any* group's call
returns an error, `PushFleetRun` cancels that shared context, which
propagates into every other group's still-running `errgroup` (each group's
`errgroup.WithContext` derives from the shared context, not from `ctx`
directly) and aborts their in-flight — and not-yet-started — hosts too. An
earlier version of this fix shared each cluster's own bounded fan-out but
left the groups' contexts fully independent, which silently narrowed
cross-cluster abort-on-failure from "whole fleet" to "one cluster",
contradicting the pre-existing contract in [Timeouts and
Cancellation](#timeouts-and-cancellation) ("a failing host cancels its
in-flight siblings ... the fleet error reports the abort reason once"); the
shared-cancellation context restores that contract while keeping the
parallelism fix, because canceling the shared context does not touch any
group's own `errgroup.SetLimit(limit)` concurrency ceiling. `cancel()` may be
called concurrently by more than one failing group; this is safe without
extra locking because `context.CancelFunc` is idempotent and
concurrency-safe (only the first call has effect). See
`TestPushFleetFailureCancelsOtherClusters` (api/cluster_test.go) for the
regression test. The push summary line (`pushed <plan> (<ops>) to <name>
(<ok>/<total> hosts)`) is still printed once per contributing member cluster
rather than once for the whole fleet.

### Remote gonf binary sync and strict preview

Before the first SSH apply chunk, every delivery to a host
(`remote.Delivery.ToHost`, used by push, cluster and fleet alike) probes the
remote gonf, in one of two modes. A strict preview (`-preview`) only verifies
it (`RequireRemoteGonf`, see below) and never changes it. A push
(`EnsureRemoteGonf`) probes `gonf -plan-version` on the target; if the remote
binary is missing or reports a plan schema older than this controller's
`CurrentVersion`, gonf cross-compiles
`github.com/snonux/gonf/cmd/gonf` for the host (via `WithGOOS` / `WithGOARCH`,
or `uname` when unset), `scp`s it, and installs to `/usr/local/bin/gonf`
(override with `WithGonfPath`). Privilege for the install follows the host's
`WithPrivilege` (root logins install without sudo/doas). Subsequent apply
commands on that push use the installed path so PATH cannot hide an older
binary.

Each gonf run normally cross-compiles once per target platform, into its own
private temp directory (`$TMPDIR/gonf-cross-<random>`, mode 0700), and reuses
that binary for every host of the same platform in the run (it rebuilds only
if one of the checks below fails). Nothing is shared
between runs, so concurrent gonf processes never ship or delete each other's
binary. Before the directory is reused, after each build into it, and before
a cached binary is handed to `scp`, gonf re-checks that the directory is still
the one it created (same inode, a real directory rather than a symlink, owned
by you, mode 0700) and that the binary is a regular file you own with the
SHA-256 recorded at build time. If not (for example a temp cleaner removed the
directory and someone planted a replacement), it logs a warning and rebuilds
in a new directory. A symlinked `$TMPDIR` (such as macOS `/var` ->
`/private/var`) is followed: gonf checks the resolved directory and creates
the build directory under it (a relative `TMPDIR` is made absolute first). gonf
refuses the build if the resolved `$TMPDIR`, or any directory above it up to
`/`, is not owned by you or root, is world-writable without the sticky bit, or
is group-writable without the sticky bit by a group other than your
user-private group (gid equal to your uid, as with the umask-002 default of
Fedora and most Linux distributions; the same rule as for `gonf plan -o`
directories), since others could swap a directory on that path. So `/tmp`,
macOS `/private/var/folders/.../T`, and home directories that are 0700, 0755,
or 0775 with your private group pass. In a rootless container, directories
from outside the user namespace show up owned by the unmapped uid 65534
(`nobody`) and are refused; point `TMPDIR` at a directory you own inside the
container. If gonf refuses, create a private directory (`mkdir -p -m 700
"$HOME/tmp"`) and export `TMPDIR=$HOME/tmp`. The directory is removed when `cli.CLI` returns
(including after SIGINT/SIGTERM/SIGHUP, and on every error return), but only while it is
still the directory gonf created, so a planted replacement is never deleted. A
crash, SIGKILL, an uncaught signal such as SIGQUIT, a second SIGINT/SIGTERM
that force-exits the outer gonf (see "Local apply cancellation" below), or a
program that pushes via `api` without `cli.CLI` leaves it for the OS temp
cleaner.

`gonf -plan-version` prints the plan schema integer (distinct from
`gonf -version`, which prints the release string). `gonf
-strict-preview-version` prints the strict-preview capability version. The
separate capability probe prevents a controller from treating an older binary
with a coincidentally matching release/schema as able to parse
`apply -strict-preview`.

`push -n` / `cluster -n` / `fleet -n` retain their established compatibility
behavior: they bootstrap a missing or stale remote gonf binary before running
`gonf apply -n`. That is a dry-run of managed resources, but it is not a
non-mutating remote preview because bootstrap can build, copy, and install a
binary.

Use `-preview` for a strict remote preview. It performs only read-only remote
version probes before running `gonf apply -n -strict-preview -`; it never
cross-compiles, SCPs, installs, or updates gonf. The remote runtime must
already report a plan schema, strict-preview capability, and release version
at least as new as the controller. A missing, unparseable, or stale runtime
fails with an instruction to run the ordinary push first. Strict preview
rejects plans with blobs rather than staging controller data on the target,
and its remote apply mode performs resource probes under dry-run semantics
without writing managed resources.

This gives the command an explicit no-bootstrap/no-managed-resource-write
contract. It does not claim that arbitrary resource probe commands are pure:
resource implementations must preserve their existing dry-run contract.

Example:

```text
gonf push -n user@host home_helix home_tmux
gonf push -preview user@host home_helix home_tmux
gonf push -- -p 2222 user@host home_helix
gonf fleet -preview frontends base commons
gonf fleet -j 2 garage garage_deploy
```

Global flags (`-profile`, `-verbose`, `-quiet`, `-dry-run` / `-n`) still apply
on the controller. `-profile` is not forwarded to the destination: it only
affects controller-side (record-time) evaluation such as opaque `When*`
checks, while the remote `gonf apply` evaluates `when_begin` guards and
renders `.Gonf` template facts from the destination's own detected facts.

`-cmd-timeout` is forwarded: a value other than the built-in `5m` reaches
the remote `gonf apply` as `gonf -cmd-timeout=<d> apply …`, so the remote
chunk's backend commands and File/ConfigSet validators run under the
controller's bound (the local elevated re-exec gets it the same way). An
older remote gonf that does not know the flag would reject the whole
command line, and neither the plan schema nor the release version tells
(binaries reporting 0.15.0 exist with and without it), so the controller
first probes the binary that will run each chunk, in its privilege context:
`gonf -cmd-timeout=<d> -plan-version` (`sudo -n`/`doas` wrapped for elevated
chunks). Only a binary that answers with its plan schema gets the flag.

That probe costs one extra ssh round trip per host and per privilege context
a chunk applies in, and for an elevated chunk one extra `sudo -n gonf …`
(or `doas`) invocation besides the apply itself. A sudoers/doas rule
restricted to `gonf apply *`, or one that requires a password, refuses it.
Whatever the reason, the chunk is then applied without the flag, under the
remote's own default, and the push is never failed by it; only the warning
differs — a gonf too old for the flag (fixed by an ordinary push, which
upgrades a stale gonf, or by reinstalling it) reads differently from a probe
that could not run gonf at all, which quotes the refusal. The two contexts
are decided separately, so an unprivileged chunk can carry the flag while
the elevated one does not. At the default timeout no probe runs at all and
the remote command is unchanged; `-preview` and `api.PushPayload` never
upgrade gonf, so there an old remote simply keeps its own default.

`gonf -list` lists **activated** tasks (After `When*` filtering for display);
plan recording still uses the full candidate set.

### Timeouts and cancellation

Three resilience knobs bound the push/fleet path; each has a narrow scope:

| Knob | Where | Bounds | Default |
|------|-------|--------|---------|
| `-host-timeout` (fleet) | per-host context | one host's **whole push** (all chunks: blob upload, applies, sticky removal) | `10m`, `0` = unlimited |
| `-o ConnectTimeout=15` (generated argv) | every `ssh` invocation | only the **TCP/SSH handshake** | 15s; an explicit `ConnectTimeout` in `ExtraSSH` / `-- ssh-args` wins (ssh uses the first option) |
| `-cmd-timeout` / `exec.Opts.Timeout` | `internal/exec` `Run`/`RunWith`/`RunWithStdin`, validators | one backend command or validator; an expired command gets SIGTERM, then SIGKILL `CancelGrace` (10s) later, so it may take timeout + 10s | `5m` process-wide; `Opts.Timeout` overrides per call (`< 0` = none); a non-default value is forwarded to the elevated re-exec and to a remote gonf that accepts the flag |

Design decisions:

- **No overall timeout on remote applies.** An apply is long by nature; the
  controller never bounds it (only the handshake via `ConnectTimeout`). The
  fleet path instead bounds the *whole push to one host* with the per-host
  timeout above, so a wedged host cannot hold an errgroup slot forever.
- **Cancellation semantics.** A failing host cancels its in-flight siblings
  (`errgroup.WithContext`); their ssh processes are killed by the context and
  the fleet error reports the abort reason once (`fleet "x": aborted: …`) —
  canceled hosts are not listed as independent failures. A host killed by its
  own per-host deadline is reported with `(host timeout after 10m0s)`. This
  contract is whole-fleet, not per-cluster: `gonf fleet` pushes each member
  cluster's hosts through its own `remote.Fanout` call (so each cluster's own
  `Parallel(n)` still bounds only that cluster's concurrency — see "Fleet
  parallelism semantics"), but `PushFleetRun` shares one
  `context.WithCancel(ctx)` across every group and cancels it the instant any
  group fails, so a failing host in one member cluster still cancels
  in-flight (and not-yet-started) hosts in every *other* member cluster of
  the same fleet push too.
- **Local apply cancellation.** `gonf <task>` and `gonf apply <plan.jsonl|->`
  (also the receiving end of a push and the elevated re-exec child) run under
  the CLI's signal context (SIGINT, SIGTERM, and SIGHUP unless it is ignored,
  so `nohup gonf …` survives a logout; `api.RunContext`,
  `api.ApplyPlanContext`). `plan.ApplyWithContext` binds that context to
  `internal/exec` for the duration of the apply, so a signal stops the
  backend command in flight, no further op starts, and the command fails
  with `interrupted: … context canceled` and exit 1. Stopping is graceful:
  the command gets SIGTERM and is SIGKILLed only after a grace period
  (`internal/exec.CancelGrace`, 10s), so an interrupted package manager can
  finish its own clean shutdown; the same grace bounds how long a grandchild
  that still holds the command's output pipes can delay the return (the
  pipes are then closed, so such a daemon gets SIGPIPE on its next write).
  In the outer (interactive) gonf a second signal force-exits it with the
  default action; that skips its deferred cleanup, so temp plan and build
  dirs (and, if an in-process chunk was mid-op, a ConfigSet lock or staging
  dir) are left behind. A `gonf apply` process (the elevated child, the push
  destination) ignores every signal after the first, since sudo routinely
  relays two, so its graceful stop and cleanup always complete.
- **Validators and interrupts.** File and ConfigSet validators are not
  stopped by the signal; they stay limited by the command timeout
  (`-cmd-timeout`), and an interrupt during one prints a one-line notice that
  gonf waits for it. After an interrupt no validator starts, and the verdict
  of one that finishes later is discarded: the op fails and the candidate is
  never published to the live file.
- **Elevated re-exec.** sudo gets SIGTERM and relays it to the elevated
  `gonf apply`, which stops like a local apply (SIGHUP too, which it gets
  when sudo's `use_pty` pty goes away). gonf prints that it waits and allows
  the command timeout plus 20s before it SIGKILLs sudo (the child runs under
  that same timeout: a non-default `-cmd-timeout` is forwarded to it), so a
  validator
  running in the child can finish and its op be aborted cleanly instead of
  an orphaned root child writing the file afterwards. doas sets the real,
  effective and saved uid to root, so an unprivileged gonf can neither
  SIGTERM nor SIGKILL a doas child: only a signal the terminal sends to the
  whole foreground process group (Ctrl-C) reaches it, and a SIGTERM sent to
  gonf alone leaves gonf waiting for the doas child to finish.
- **No end-to-end remote cancel.** Canceling a push (`push`, `cluster`,
  `fleet`) kills only the local `ssh`. Nothing signals the remote `gonf
  apply`: it keeps applying until its next write to the now closed stdout
  fails.

Plan schema **version 11** extends `if_changed` / `watch` from
`daemon_reload` to `command`, `service`, and `timer` ops. `OnChange(res…)`
records both the watched IDs and normal dependency IDs: commands run only
after a watched change, while services and timers still converge state but
hold requested restart/reload actions until a watched change. An older binary
would ignore these fields on the newly supported kinds and run the action on
every apply, so v10 binaries refuse v11 plans at the header gate before any
mutation. Change reports are privilege-chunk-local; controller-side plan
validation rejects a watched op in another chunk or an empty/dangling watch.

Plan schema **version 12** adds `template_data` to `file` ops. Older binaries
must refuse these plans: otherwise they would render a template without its
declared data and either fail or silently produce incorrect output. Version 12
also makes destination facts available beneath the reserved `.Gonf` template
context.

Plan schema **version 13** adds additive-only `user` operations. It records
the requested primary and supplementary groups plus creation-time home, shell,
login-class, and system-account settings. Older binaries must refuse these
plans rather than silently skipping the unknown resource kind.

Plan schema **version 14** adds `ensure_file` and the `add_lines` /
`remove_lines` arrays on `file` operations. `EnsureFile` makes the
existence decision on the destination: it creates a missing empty regular
file but preserves the bytes of an existing regular file while explicit
metadata converges. The arrays preserve declaration order and allow one
resource to apply a complete batch of line edits; older singular line fields
remain accepted when applying older plans.

Plan schema **version 15** extends `env` from `command` to `package`
operations. `WithEnv` is applied to both the package manager's state probe
and its mutation, so older binaries must reject v15 plans rather than silently
using their inherited environment for package operations.

Plan schema **version 16** makes `name` an explicit `file`-operation identity
as well as a command/package label. `File(path, WithName(name), ...)` still
manages `path`, but it registers and reports as `File[name]`, allowing several
line-edit declarations for one path and precise `DependsOn` / `OnChange`
wiring. An older binary would ignore `name` for a file, report its change as
`File[path]`, and leave an `OnChange(File[name])` action permanently skipped;
it must therefore reject v16 plans at the header gate before any mutation.

Plan schema **version 17** adds `legacy_command` to `cron` operations. It
identifies an exact legacy unmanaged cron command that the destination removes
while adopting the named Gonf block. An older binary would leave that command
running alongside the managed block, so it must refuse v17 plans before any
mutation.

Plan schema **version 18** adds `validation_bin` and `validation_args` to
content-managed `file` operations. The destination writes a private candidate,
substitutes its path for the one `CandidatePath` argument, and runs the argv
directly before it can publish the live file. An older binary would ignore the
validator fields and publish unvalidated content, so it must refuse v18 plans
before any mutation.

Plan schema **version 19** adds `manage_home` to `user` operations.
`User(name, WithHome(home), WithManageHome)` converges an existing account's
passwd home field (never its directory contents) to `home`. An older binary
would ignore the field and report the account as converged while leaving the
old home in place, so it must reject v19 plans at the header gate before any
mutation. Plans without the opt-in encode the `user` operation exactly as in
v13–v18.

Plan schema **version 20** adds `require` to `when_begin`. A requirement block
refuses instead of skipping: when its enclosing scope is active but its
predicates do not hold, `Apply` fails with
`<id>: requirement not met on this host (goos=<destination GOOS>): <require>`.
A requirement's own predicates and every `when_begin` enclosing it must be
host facts (`goos`, `profile`, `hostname_contains`): they evaluate the same in
every privilege chunk and cannot change while ops run. A requirement nested
under `path_exists` (filesystem state, which can change mid-apply or differ
for the elevated user) or any other condition is refused when the plan is
recorded (`ValidateChunks`, so `gonf plan`, run, push, cluster and fleet
never produce one) and again by `Apply`'s pre-check for hand-written plans,
with an error naming the requirement and the offending condition. Given that
rule, all requirements of a plan (or chunk) are decided before the first
mutation, in dry runs too, and `SplitPrivilegeChunks` copies each requirement
as an empty stub (inside its host-fact openers) to the front of every earlier
chunk, so a mixed-privilege plan refuses before its first chunk writes.
`LoginClass` is the first user (`goos == openbsd`). An older binary would
silently skip the block, so it must refuse v20 plans before any mutation.

Plan schema **version 21** adds the `config_set` and `config_set_member` kinds
(see [config-set.md](config-set.md)). A `config_set` line carries `members`
(key, path, `content_b64`, mode, owner, group), argv `validators`, and the
optional `chroot` and `staging_dir`; a `config_set_member` line is the
report-only handle of one member (`name` = set, `member` = key) that
`OnChange` can watch. An older binary would reach the unknown kind only
mid-apply, after earlier operations already ran, so it must refuse v21 at the
header gate. Plans without a config set encode every other operation exactly
as in v20.

Plan schema **version 22** adds `sensitive` to resource operations: the op's
payload (`content_b64` or its blob, `template_data`, member contents, lines,
argv, environment) holds secret material. The controller sets it while
recording (see "Secret material" below); the destination then withholds a
failing file or `config_set` validator's output, template error details and
a command's argv and failure output. An older binary would ignore the field
and could echo the secret, so it must refuse v22 plans at the header gate.
Only a plan with a sensitive op declares v22 (`plan.RequiredVersion`); a
plan without secret material keeps a v21 header and encodes every operation
exactly as in v21. `push` and strict preview keep comparing the remote
runtime with the controller's own schema and release (`-plan-version`,
`-version`), so push installs a current gonf and strict preview refuses an
older remote either way.

### Secret material

`MustSecret(path)` reads a required non-empty file below the controller
recipe's `secrets/` directory; `OptionalSecret(path)` returns `(value, false)`
when that file is absent, which is useful for optional per-host plan fragments.
Both preserve bytes exactly (including newlines), reject paths that escape the
secrets directory and any symlink in the secret path, and report only the path
and failure class—never secret contents. A leading `/` remains below `secrets/` for Rex compatibility, so
`MustSecret("/var/nsd/key")` reads `secrets/var/nsd/key`. That file reading is
the default `secret.FileProvider`; a consumer may configure another provider
once with `SetSecretProvider`, and `ResolveSecret` returns bytes with a typed
error. The provider contract, its error kinds and the optional-means-not-found
rule are in [secrets.md](secrets.md). No provider changes what is recorded:
only what a recipe places into a resource reaches the plan.

Every value the provider returns is remembered for the rest of the process,
and an op that carries one — verbatim, trimmed, embedded in rendered text, or
inside template data — is recorded with `sensitive: true` (v22);
`SecretFile(path, ref)` is the typed entry point for a file that is exactly
one secret. Sensitivity changes how the plan is handled, not what it holds:
the secret is still in clear text (base64 is an encoding, not encryption) in
the owner-only (`0600`) `plan.jsonl`, in the in-memory push payload and in
the encrypted SSH transport, because the destination must write it.

- `gonf plan -stdout` refuses a plan with sensitive ops (it names them);
  `-stdout -with-secrets` prints it anyway as an explicit export.
- `gonf plan -redacted` prints a human preview: the header op is
  `plan_preview` (no gonf applies it) and secret material is `[redacted]`.
- `gonf plan -o dir` warns on stderr that `plan.jsonl` is an executable
  secret artifact; delete it once applied. gonf keeps no other copy.
- A failing validator's output, template error details and a command's argv
  and failure output are withheld on the destination for sensitive ops;
  debug logs never show content digests. An op whose identity (ID, name,
  path, binary — e.g. an unnamed `Command`'s argv) holds a strong secret
  (8+ bytes and not word-like) is refused at record time; every string field
  of every op, control ops included, is scanned (see secrets.md for the
  classes).
- A multi-chunk push refuses a sensitive blob-backed op in an elevated
  chunk, because its sticky blob directory belongs to the SSH login user.

The full lifecycle and its limits are in [secrets.md](secrets.md). Never put
secret values in task names, descriptions, paths or host values: identities
are logged everywhere and are not redacted.

Plan schema **version 10** adds the `latest` field to `package` ops: a
`Package` recorded with `IsLatest` now carries that intent explicitly, so
destination apply runs the backend's upgrade-check path (`dnf update` / `pkg
upgrade` / `pkg_add -u` / `pkgin install`) instead of a plain install, even
when the package already shows as installed. An older binary that ignored
`latest` would silently drop the upgrade — the package would be reported OK
and never upgraded — so v9 binaries refuse v10 plans up-front at the header
gate instead, while this binary keeps applying v1–9 plans. Plan schema
**version 9** adds the `template` / `template_param` fields to
`file` ops: a `File` recorded from a `.tmpl`-suffixed source or path now
carries that intent explicitly, so destination apply renders it instead of
writing the raw template text (the `path`/`content_b64` on the wire no
longer carry a usable `.tmpl` suffix by apply time — see "Recording" above).
An older binary that ignored `template` would write the literal
`{{...}}`-style content to disk — a silent, user-visible content-loss bug,
the same intent-loss class as previous bumps — so v8 binaries refuse v9
plans up-front at the header gate instead, while this binary keeps applying
v1–8 plans. Plan schema **version 8** adds the `in` field to `when_begin` predicates: an
OR-list of acceptable fact values (e.g. `WhenProfile(a, b)` now lowers to
`{"fact":"profile","in":["a","b"]}` instead of being treated as opaque — see
"Recording" above). An older binary that ignored `in` would evaluate the
predicate against its empty `eq` and treat it as never matching — the same
intent-loss bug class as previous bumps — so v7 binaries refuse v8 plans
up-front at the header gate instead, while this binary keeps applying v1–7
plans. Plan schema **version 7** adds the `systemd_timer` op: declarative
install of a `.timer` + companion oneshot `.service` (command, OnCalendar, optional
OnBootSec/Persistent/descriptions/After/Wants). An older binary that lacked
the kind would fail at apply with `unknown op`; the version gate refuses v7
plans up-front instead, while this binary keeps applying v1–6 plans. Plan
schema **version 6** adds the `source_dir` field to `sync_dir` ops: the
recipe's declared source directory (the glob pattern's directory for the glob
flavor) so destination apply renders tree `.tmpl` files' `{{.Param}}` from the
stable declared identity instead of the per-run blob path. The bump follows
the deps-field rationale: an old binary that ignored the field would render
`{{.Param}}` from the ephemeral blob-extraction path — user-visible, changing
every run (the same intent-loss class) — so v5 binaries refuse v6 plans
up-front at the header gate instead, while this binary keeps applying v1–5
plans. Version 5 added the `deps` field to resource ops: the sorted
resource IDs a resource depends on (its `DependsOn` targets, e.g.
`File[/etc/foo]`). With deps present, remote apply order matches the
repository's topological order; ops are never reordered across `when_*`
boundaries. The bump follows the owner/group bump rationale: an old binary
that understood a dep-free schema would silently DROP dep ordering — the same
intent-loss bug class — so v4 binaries refuse v5 plans up-front at the header
gate instead, while this binary keeps applying v1–5 plans. Version 4 added
`owner` / `group` fields to the filesystem ops (`file`, `dir`, `sync_dir`,
`ensure_dir`): ownership explicitly set via `WithOwner` / `WithGroup` is
enforced on destination apply (only explicitly configured ownership is
recorded; empty fields leave ownership to the apply side). Version 3 added
`cron` and `service` ops; version 2 added `timer` and `daemon_reload` ops.

Privilege-chunked plans (mixed privileged/unprivileged ops) carry deps
across the chunk boundary in dependency order: chunks apply in recorded
order and never reorder, so a dep naming an op from an EARLIER chunk is
satisfied (the earlier chunk applied it first). A dependency recorded AFTER
its dependent crosses the privilege boundary — apply cannot reorder across
chunks — and is rejected before anything is applied by a controller-side
pre-flight (`plan.ValidateChunks`, run at record time and by `ApplyChunks` and
`remote.Delivery.ToHost`); on push or preview the refusal happens before any
SSH traffic. The same pre-flight refuses dangling deps (naming no recorded
resource at all).
Executing or pushing a single chunk (`api.ApplyPlan`, `api.PushPayload`,
`api.PushPayloadContext`, `gonf apply <plan.jsonl|->`) does not re-run it.
For recorded plans (`Run`, `gonf plan`, push, cluster, fleet, `gonf apply`)
the chunk order stays fixed by recorded order: chunks are never reordered
after the split. `api.Apply` is the exception: its ops have no meaningful
recorded order (its drafts are sorted by resource ID), so when an op is
elevated it chooses the order itself BEFORE splitting: a dependency sort
with as few chunks as its dependencies and change watches allow (see
"Low-level `Apply()`"). After
that sort a forward cross-chunk dep can only come from a dependency cycle
(a resource depending on itself included), which Apply refuses, naming the
cycle, before any chunk applies.

## JSONL sketch

```jsonl
{"op":"plan","version":1,"id":"demo"}
{"op":"link","path":"${HOME}/.bashrc","symlink":"/path/to/bashrc"}
{"op":"when_begin","all":[{"fact":"goos","eq":"linux"}]}
{"op":"file","path":"${HOME}/.taskrc","mode":"0640","content_b64":"Li4u"}
{"op":"file","path":"${HOME}/secret.conf","mode":"0640","owner":"paul","group":"1000","content_b64":"Li4u"}
{"op":"when_end"}
{"op":"command","bin":"systemctl","args":["--user","daemon-reload"],"unless":{"bin":"true"},"deps":["File[/etc/foo]"]}
```

Schema version is the wire format version (not the gonf app version). Bump it
when ops/fields change meaning; old apply binaries reject newer plans cleanly.

## Low-level `Apply()`

`api.Apply()` snapshots registered resource drafts and uses the same plan
engine as `Run`, including dependency ordering and source/blob packaging.

It also honours the privilege split like `Run` and `gonf apply`. When an op
is elevated (a `Command` with `WithElevate`), Apply sorts the ops by
dependency, keeping each privilege class together and keeping a change-gated
resource (`OnChange` / `WatchChanges`) in the same chunk as the resources of
its class that it watches. Each resource gets the earliest chunk its
dependencies and watches allow; Apply tries this starting with each class
and keeps the result with fewer chunks, which is the fewest possible under
those rules. It then splits the ops with `plan.SplitPrivilegeChunks` and
applies the chunks in order, as `api.ApplyChunks` does, under the
process-wide privilege mode (`api.SetPrivilege`, the CLI `-privilege` flag).
A chunk that fails is named by its class and resources, e.g.
`Apply: elevated resources Command[e]: ...`, not by a chunk index. An
elevated chunk is re-executed as `<this binary> apply <chunk>` through
sudo/doas, or runs in-process when the mode is `none` and the process is
already root. Before this, an elevated op under `api.Apply` silently ran
in-process as the calling user. A plan with no elevated op is not sorted:
it is one unprivileged chunk and applies exactly as before, with a single
`api.ApplyPlan` call and the same error wording.

With an elevated op, Apply refuses the plan before ANY chunk applies (so
nothing runs as root, and no unprivileged chunk mutates first) when:

- the ops have a dependency cycle. The error names it, e.g.
  `Apply: circular dependency: Command[x] -> Command[y] -> Command[x]`. A
  resource depending on itself is the smallest cycle
  (`Command[b] -> Command[b]`);
- the privilege mode is `none` and the process is not root;
- the process does not run gonf's CLI (`cli.CLI()`). The re-exec runs
  `<this binary> apply <chunk>`, and in a program with its own `main` that
  would run the program again as root, which would apply and re-exec again.
  The CLI sets an internal marker (`internal/clihost`) and the elevated path
  refuses without it.

The last two checks live in `api.ApplyChunksContext`, so `Run` (and anything
else applying recorded chunks locally) refuses the same way before its first
chunk.

Because it applies the whole plan, Apply first runs the dependency and
change-gate pre-flight itself over those chunks. It does not record through
`RecordPlanTo`, so the record-time check does not cover it. A `DependsOn`
(or `OnChange` / `WatchChanges`) naming a resource that is not registered fails
with an `Apply:` error naming the op and the missing ID before anything is
applied. The error unwraps to `*plan.DanglingDepError` / `*plan.DanglingWatchError`.

Changed state is not carried across chunks: each chunk applies as its own
`plan.Apply` run with a fresh change report (the elevated one usually in a
separate sudo/doas process, in-process only when the mode is `none` and the
process is root), so a change gate only sees changes from its own chunk.
With elevated ops, a watch that cannot stay in one chunk is therefore
refused before anything applies, as `Run` refuses it, and the error names
both privilege classes instead of chunk indexes:

- across classes, in either direction: an unprivileged command gated
  `OnChange` of an elevated one, or an elevated one (such as an elevated
  daemon-reload) watching an unprivileged user file, e.g.
  `Apply: Command[reload] (elevated) watches File[...] (unprivileged); change
  reports are not carried across privilege classes ...`;
- within one class, when dependencies force an elevated resource between the
  two (the gated resource needs an elevated one that itself needs the watched
  resource): `... watches File[f] (unprivileged), but their dependencies
  need resources of the other privilege class applied between the two ...`;
- within one class, when the watch would fit alone but not together with
  watches Apply already kept. Apply keeps watches in declaration order, each
  one only if it still fits with those kept before it, and the error names
  a minimal set of kept watches it conflicts with (without any one of them
  it would fit): `... watches Command[d] (unprivileged), but together with
  the change watch Command[a] watching Command[b], their dependencies need
  resources of the other privilege class applied in between ...`, or
  `together with the change watches A watching B and C watching D, ...`.

The lower-level `resource.Apply()` path remains for resource-package unit tests
and ad-hoc compatibility use; new application code should prefer `Run` or
`api.Apply` so local and remote execution share the plan engine.
