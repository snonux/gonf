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
chunk, so a dep missing from the body must count as satisfied there. The check
(`plan.ValidateChunkDeps`) therefore runs wherever the **whole plan** is in
hand, before anything is applied or uploaded:

| Where | Covers |
|-------|--------|
| record time (`api.RecordPlanTo`) | `gonf <task>`, `gonf plan`, push, cluster, fleet — the plan never gets written, shipped or applied |
| `api.ApplyChunks` | local apply of an already-recorded plan |
| `remote.PushChunks` | SSH push, before any SSH traffic |
| `api.Apply` | registered resources (the whole plan as one chunk) |

It does **not** run when a single chunk or plan file is executed: `api.ApplyPlan`,
`plan.Apply` and `gonf apply <plan.jsonl|->`. A plan recorded by a current gonf
was already checked; a hand-written or older plan file applied that way gets no
dangling-dependency protection.

## Why

Tasks are Go code. Shipping whole Go task trees to every host is awkward.
Instead the controller **records** task bodies into a portable plan; the
destination only needs a gonf binary that understands the plan schema.

Local `Run` / `gonf <task>` uses the same path automatically (temp dir for
blobs, then apply, then cleanup) so local and remote cannot diverge.

## Error handling contract

gonf separates **registration-time** misuse from **runtime** failures:

- **Registration-time DSL misuse fails fast** via `internal/logger.Fatal`
  (process exit 1): duplicate `Task` / `Host` / `Fleet` registration, an
  option applied to a resource that does not support it (`options.requires`,
  e.g. `*file.File does not support WithRestart`), invalid option combinations
  (`WithLine` + `WithContent`, `WithSource` + `WithSourceGlob`), an invalid
  `Matching` pattern, or `MustHost` / `MustCluster` lookups of unknown names.
  These are programmer errors in the recipe; nothing has been recorded or
  applied yet, so aborting immediately is the honest outcome.
- **Record-time failures return errors**: unknown tasks, recursion cycles,
  packaging failures, registered resources without plan drafts, dangling or
  cross-privilege-chunk `DependsOn` / `OnChange` targets, and a failing
  child task inside an `Aggregate` all fail `RecordPlan` / `Run` with a
  returned error. Task bodies cannot return errors, so Aggregate stashes the
  failure (`stashBodyError` in `api/plan.go`, same mechanism as the cycle
  stash) and the enclosing record fails with `aggregate <name>: <cause>`.
  Nothing is applied in that case: the abort happens during recording, before
  plan apply runs.
- **Secret failures return record-time errors**: `MustSecret` and
  `OptionalSecret` load controller-local `secrets/<path>` files while a task is
  recorded. Required missing secrets, empty secrets, unreadable files, and
  unsafe paths fail before local apply or SSH push; an optional missing file
  simply lets the recipe omit that host's fragment. This preserves deferred
  plan-directory cleanup and prevents a partial plan from reaching a host.
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

Residual: a registration-time Fatal fired from *inside* a task body still
skips `Run`'s deferred temp-plan-dir cleanup (the directory lives under
`$TMPDIR`). That is the accepted cost of the fail-fast DSL contract.

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
- `SyncDir` trees → `planDir/blobs/<name>/`.
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
- Nested `Run` while recording (e.g. `Aggregate`) appends into the **same**
  plan; apply happens once at the top level.

### Helpers that emit recipes (not controller Stat)

| API | Plan op |
|-----|---------|
| `WhenLinux` / `WhenProfile` / `WhenHostnameContains` | `when_begin` with fact predicates |
| `WhenPathExists(path, fn)` | `when_begin` with `path_exists` around `fn` |
| `WhenHostname(substr\|List(...), fn)` | `when_begin` with `hostname_contains` around `fn` (`List` → one block per entry) |
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
  typo'd one; the controller-side pre-flight (`plan.ValidateChunkDeps`, run at
  record time and by `ApplyChunks`, `remote.PushChunks` and `api.Apply`, but
  not when a single chunk is executed — see "Where dependencies are checked")
  refuses forward cross-chunk and dangling deps before anything is applied.
- Stackable `when_begin` / `when_end`: failed predicates skip the body
  without touching the filesystem.
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
from an `init()`; `api/plan.go`'s `draftToOp` and `plan/apply.go`'s
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
three kinds, `link` registers two). `api/plan.go`'s `draftToOp` and
`plan/apply.go`'s `applyActive` are now pure `HandlerFor` dispatch with no
fallback switch cases and no resource-package imports of their own — this is
also what lets `plan` be a pure types/codec/split/pushwire/blob-store
package (`go list -deps ./plan/...` pulls in only the resource-neutral
`resource` core package, never a `resource/<kind>` backend). Steps 2 and 5
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
   re-deriving `opt.Option`s from `Op` fields by hand.
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
   the new `plan.Op`. `api/plan.go`'s `draftToOp` only ever calls
   `HandlerFor(d.Kind)`; an unmapped kind errors at record time — there is
   deliberately no silent default and no per-kind case left to add there.
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
takes. A resource package that adds a `planwire.go` also means any
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
| `gonf plan [-o dir\|-stdout] [-id name] <task>…` | Write `dir/plan.jsonl` (+ `blobs/`), or print JSONL to stdout |
| `gonf apply [-n\|-dry-run\|-strict-preview] <plan.jsonl\|->` | Apply a plan file, or read **GONF-PUSH/1** / bare JSONL from stdin |
| `gonf push [-n\|-preview] [-id name] [-- ssh-args…] user@host <task>…` | Record in memory, stream over `ssh` to remote `gonf apply -` |
| `gonf cluster [-n\|-preview] [-j N] [-id name] [-host-timeout 10m] <cluster> <task>…` | Resolve inventory cluster; record once; parallel push or strict preview to each host |
| `gonf fleet [-n\|-preview] [-j N] [-id name] [-host-timeout 10m] <fleet> <task>…` | Resolve fleet (list of clusters); push or strict preview on unique hosts |
| `gonf hosts` / `gonf clusters` / `gonf fleets` | List registered inventory |

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

Before the first SSH apply chunk, `PushChunks` probes `gonf -plan-version` on
the target. If the remote binary is missing or reports a plan schema older
than this controller's `CurrentVersion`, gonf cross-compiles
`github.com/snonux/gonf/cmd/gonf` for the host (via `WithGOOS` / `WithGOARCH`,
or `uname` when unset), `scp`s it, and installs to `/usr/local/bin/gonf`
(override with `WithGonfPath`). Privilege for the install follows the host's
`WithPrivilege` (root logins install without sudo/doas). Subsequent apply
commands on that push use the installed path so PATH cannot hide an older
binary.

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

Global flags (`-profile`, `-verbose`, `-quiet`, `-dry-run` / `-n`) still apply.
`gonf -list` lists **activated** tasks (After `When*` filtering for display);
plan recording still uses the full candidate set.

### Timeouts and cancellation

Three resilience knobs bound the push/fleet path; each has a narrow scope:

| Knob | Where | Bounds | Default |
|------|-------|--------|---------|
| `-host-timeout` (fleet) | per-host context | one host's **whole push** (all chunks: blob upload, applies, sticky removal) | `10m`, `0` = unlimited |
| `-o ConnectTimeout=15` (generated argv) | every `ssh` invocation | only the **TCP/SSH handshake** | 15s; an explicit `ConnectTimeout` in `ExtraSSH` / `-- ssh-args` wins (ssh uses the first option) |
| `exec.Opts.Timeout` | `internal/exec` `RunWith` | one external command (opt-in per call) | `0` = no timeout (historical behavior) |

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
- **What is not context-aware (yet).** Local apply and single-host `push` run
  without a signal context, and `exec.Run` / the resource packages have no
  timeouts: a wedged local `dnf`/`systemctl` still blocks. The
  `exec.Opts.Timeout` field exists for opt-in callers; wiring it globally was
  deliberately deferred (it would change apply semantics).

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

### Secret material

`MustSecret(path)` reads a required non-empty file below the controller
recipe's `secrets/` directory; `OptionalSecret(path)` returns `(value, false)`
when that file is absent, which is useful for optional per-host plan fragments.
Both preserve bytes exactly (including newlines), reject paths that escape the
secrets directory and any symlink in the secret path, and report only the path
and failure class—never secret contents. A leading `/` remains below `secrets/` for Rex compatibility, so
`MustSecret("/var/nsd/key")` reads `secrets/var/nsd/key`.

When secret bytes are passed to `WithContent`, they are managed material:
they are present in clear text in the owner-only (`0600`) `plan.jsonl` output
and in the encrypted SSH transport payload. Do not use `gonf plan -stdout` for
such a recipe: stdout is easily logged, redirected, or copied. Never put secret
values in task names, descriptions, or host values.

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
pre-flight (`plan.ValidateChunkDeps`, run at record time and by `ApplyChunks`,
`remote.PushChunks` and `api.Apply`); on push the refusal happens before any SSH
traffic. The same pre-flight refuses dangling deps (recorded in no chunk).
Executing a single chunk (`api.ApplyPlan`, `gonf apply <plan.jsonl|->`) does not
re-run it. Elevation ordering stays fixed by recorded order; reordering across
chunks would defeat the privilege split.

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
Because it applies the whole plan, it first runs the dangling-dependency
pre-flight itself (the plan as one chunk; it does not record through
`RecordPlanTo`, so the record-time check does not cover it): a `DependsOn`
naming a resource that is not registered fails with an `Apply:` error naming the
op and the missing ID before anything is applied.
The lower-level `resource.Apply()` path remains for resource-package unit tests
and ad-hoc compatibility use; new application code should prefer `Run` or
`api.Apply` so local and remote execution share the plan engine.
