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
  packaging failures, registered resources without plan drafts, and a failing
  child task inside an `Aggregate` all fail `RecordPlan` / `Run` with a
  returned error. Task bodies cannot return errors, so Aggregate stashes the
  failure (`stashBodyError` in `api/plan.go`, same mechanism as the cycle
  stash) and the enclosing record fails with `aggregate <name>: <cause>`.
  Nothing is applied in that case: the abort happens during recording, before
  plan apply runs.
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
- Every draft must map through an explicit `draftToOp` case in `api/plan.go`:
  an unknown draft kind fails `RecordPlan` loudly at record time (there is no
  `default:` passthrough to the wire), so a typo or a forgotten case is a
  controller-side record error instead of a remote apply-time `unknown op`.
- `InstallFile` content → `content_b64` when ≤ 512 KiB, else a blob sidecar.
  A legitimately empty file (`WithContent("")` or an empty `WithSource` file)
  also encodes as `content_b64:""`, so the `file` op carries `has_content:true`
  whenever content/source was configured at all; apply treats `content_b64`
  and `blob` both empty **and** `has_content` false as a record-time bug
  ("missing content_b64 and blob"), not as an empty file.
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
  Controller-side pre-flight (`plan.ValidateChunkDeps`, wired into
  `ApplyChunks` and `remote.PushChunks`) refuses forward cross-chunk and dangling
  deps before any chunk is applied.
- Stackable `when_begin` / `when_end`: failed predicates skip the body
  without touching the filesystem.
- Expands `${HOME}` on the destination; unknown `${…}` is a hard error.
- Maps ops to existing resource `Ensure` helpers (`file`, `dir`, `link`,
  `cmd`, `pkg`, …) — same semantics as direct resource APIs.

## Adding a resource kind (checklist)

A new plan-pushable resource kind touches ~8 places across 6 files. The
fitness tests make every step discoverable: adding a `plan.Kind` without the
record-side mapping or fixtures fails `TestPlanKindFitness` (api),
`TestApplyActiveKnowsEveryResourceKind` (plan), and the AllKinds inventory
test (`plan/types_test.go`).

1. **Wire type** — `plan/types.go`: add the `KindX` const and its `allKinds`
   entry (this is the exhaustiveness inventory); add any new `Op` payload
   fields with `omitempty`. If fields change meaning (not just grow), bump
   `CurrentVersion` and add golden fixtures under `plan/testdata/`.
2. **Apply handler** — `plan/apply.go`: add the `applyActive` case plus an
   `applyX` handler that validates required fields (e.g. `name`, `path`,
   `schedule`) before any mutation, then delegates to the resource `Ensure`.
3. **Resource draft** — the resource package: set the new draft `Kind` string
   in its `planDraft()` and call `resource.RecordPlanDraft` from `Present`
   (the register-without-draft guard fails the record otherwise). Map absent
   resources onto the same kind with `Absent: true` (see `NoCron`/`NoService`).
   Include `Deps: x.DependsOn.SortedIDs()` so `DependsOn` ordering survives
   the wire.
4. **Draft payload** — `resource/draft.go`: add any new `PlanDraft` fields the
   kind needs (package-neutral, no `plan` import — resource packages must not
   depend on the wire codec).
5. **Lowering** — `api/plan.go` `draftToOp`: add the `case "<draft_kind>"`
   mapping to `plan.KindX`. Unmapped kinds now error at record time; there is
   deliberately no silent default.
6. **Options capability** — `api/options`: add the task-level knobs
   (`With*` options + the interface the resource implements), following the
   existing small-interface pattern.
7. **Fitness fixtures** — `api/plan_fitness_test.go`: add a `kindFitnessTable`
   entry (draft fixture + round-trip) for the new kind. The apply-side
   dispatch is pinned by `TestApplyActiveKnowsEveryResourceKind`
   (`plan/apply_fitness_test.go`) via `AllKinds()` automatically.
8. **Behaviour tests** — record-side lowering in `api/plan_lower_test.go`
   (kind, payload, IDs stable for `DependsOn`) and apply-side behaviour in
   `plan/apply_test.go` / `plan/e2e_test.go`; update `docs/plan.md` tables
   (recipe table above, schema version notes).

Do not add self-registered kind codecs (a map consulted by
`draftToOp`/`applyActive`): the explicit switch cases are the current design;
revisit only if the kind count makes the checklist unmanageable.

## CLI

| Command | Effect |
|---------|--------|
| `gonf <task> [task…]` | Record + apply locally |
| `gonf plan [-o dir\|-stdout] [-id name] <task>…` | Write `dir/plan.jsonl` (+ `blobs/`), or print JSONL to stdout |
| `gonf apply [-n\|-dry-run] <plan.jsonl\|->` | Apply a plan file, or read **GONF-PUSH/1** / bare JSONL from stdin |
| `gonf push [-n] [-id name] [-- ssh-args…] user@host <task>…` | Record in memory, stream over `ssh` to remote `gonf apply -` |
| `gonf cluster [-n] [-j N] [-id name] [-host-timeout 10m] <cluster> <task>…` | Resolve inventory cluster; record once; parallel push to each host |
| `gonf fleet [-n] [-j N] [-id name] [-host-timeout 10m] <fleet> <task>…` | Resolve fleet (list of clusters); push to unique hosts across all members |
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
in parallel (errgroup limit from the fleet or `-j`). The fan-out runs under a
context: a failing host **cancels its in-flight siblings** (their ssh
processes are killed, and they are reported as aborted, not as independent
failures), and `gonf fleet` threads its signal-derived context so
SIGINT/SIGTERM abort the whole push. See [Timeouts and
Cancellation](#timeouts-and-cancellation).

### Remote gonf binary sync

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
`gonf -version`, which prints the release string).

Example:

```text
gonf push -n user@host home_helix home_tmux
gonf push -- -p 2222 user@host home_helix
gonf fleet -n frontends base commons
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
  own per-host deadline is reported with `(host timeout after 10m0s)`.
- **What is not context-aware (yet).** Local apply and single-host `push` run
  without a signal context, and `exec.Run` / the resource packages have no
  timeouts: a wedged local `dnf`/`systemctl` still blocks. The
  `exec.Opts.Timeout` field exists for opt-in callers; wiring it globally was
  deliberately deferred (it would change apply semantics).

Plan schema **version 8** adds the `in` field to `when_begin` predicates: an
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
pre-flight (`plan.ValidateChunkDeps`, wired into `ApplyChunks` and
`remote.PushChunks`); on push the refusal happens before any SSH traffic. The same
pre-flight refuses dangling deps (recorded in no chunk). Elevation ordering
stays fixed by recorded order; reordering across chunks would defeat the
privilege split.

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

`api.Apply()` / registering resources then `resource.Apply()` still exists for
unit tests and ad-hoc resource use. Normal task execution goes through the
plan engine above — do not rely on the register-then-`Apply` path for configs
you also want to run remotely.
