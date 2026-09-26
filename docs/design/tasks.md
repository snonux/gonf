# Tasks, Facts, and CLI

gonf configs are Go programs: register **tasks**, then run them via `cli.CLI()` or
`Run(...)`. Execution always goes through the **plan → apply** engine — see
[plan.md](plan.md).

> 🦫 **Gonfy says:** A task is one chore on my list. Name it, give it a body, and run it whenever the lodge needs it.

## Task

```go
Task("hello", "Say hello", func() {
    File(Home(".hello"), WithContent("hi\n"))
}, WhenLinux())
```

`Task` queues a candidate. Serializable `When*` options become `when_begin`
recipes in the plan and are evaluated on the **destination** at apply time
(local or remote). Opaque `When(func(Facts) bool)` cannot travel in a plan;
recording requires them to pass on the controller.

### Destination guards (task 8h2)

A serializable guard (`WhenLinux`, `WhenProfile`, `WhenHostnameContains`) is a
question about the destination, so the controller does not answer it when
deciding which tasks exist. Only opaque predicates (`When(func)`, a
`RegisterMethods` `WhenFoo` companion, a `WhenProfile()` without profiles)
activate or hide a task on the controller. A task whose serializable guard
does not hold on the controller is still listed, still a pattern-aggregate
and `AggregateTasks` member, and its ops travel inside its `when_begin`, so
`gonf push host home` records a member guarded for `host` even from a
controller the guard excludes: record once, evaluate per destination.

This is an approved behaviour correction (approved by the user 2026-09-24,
task 8h2). Before it, the controller also filtered by serializable guards,
so a push silently dropped every aggregate member whose guard only matched
the destination (dotfiles' former `home_tmux_rocky`, pushed from earth), and
a `WithGroupWhen(WhenProfile(...))` group pushed from a controller of
another profile lost all its tasks.

| Task guards | Controller (activation, `-list`, aggregates) | Destination |
|---|---|---|
| none | active | always applies |
| serializable only | active; `-list` marks it `[destination-guarded: …]` when the guard does not hold here | `when_begin` decides |
| opaque only | active only when the predicate holds here | no guard travels; push refuses the plan |
| mixed | active only when the opaque part holds here | the serializable part decides |

Naming a guarded task explicitly records it with its `when_begin`, as
before. An `Alias` carries its target's guard (and mark), and a member
reached through an alias or a nested aggregate records exactly like the
member named directly.

**Local runs** (`gonf home` on the machine itself, `Run`) keep their outcome
and their plan unchanged: their destination is this host, so an aggregate
resolves each member's serializable guard against this host's facts at
record time and skips a member it excludes (a `-verbose` debug line names
it), exactly as before. Recording that member into a `when_begin` instead
would converge the same resources, but a `Privileged()` member that cannot
apply here would still split off an elevated chunk — a sudo/doas re-exec
that changes nothing, an extra empty summary, or a refusal under
`-privilege none`. The summary counts and output wording of a local run are
therefore identical. `gonf plan` and every push, cluster or fleet run record
the member inside its `when_begin`, so their op counts grow by the formerly
dropped members.

| Helper | Meaning |
|--------|---------|
| `When(pred)` | Custom `func(Facts) bool` (not serializable) |
| `WhenLinux()` | Plan recipe: `goos == linux` |
| `WhenProfile("fedora", "rocky")` | Plan recipe: match `Facts.Profile` |
| `WhenHostnameContains("laptop")` | Plan recipe: hostname substring |
| `WhenPathExists(path, fn)` | Plan recipe: `path_exists` around `fn` |
| `WhenHostname(substr\|List(...), fn)` | Plan recipe: `hostname_contains` around `fn` (body-level counterpart of `WhenHostnameContains`). `List(...)` expands to one fragment per entry — same as looping — so identical per-host bodies stay DRY |

Combine fact predicates with `And` / `Or` from [helpers.md](helpers.md) for
custom `When` only — prefer the named helpers when you need remote plans.

## Body vs task options: WHAT vs the execution contract

A task has two distinct parts, answered at different moments:

| | Task body (`func()`) | Task options (`TaskOption`s) |
|---|---|---|
| Answers | **WHAT** the desired state is | **WHEN / WHERE / AS WHOM** it may be enforced |
| Holds | resources: `File`, `Cron`, `Package`, … | `When*` gates (where), `Privileged()` (as whom) |
| Analogy | the payload / manifest | the envelope / serving contract |

The engine — not the recipe — derives the *mechanics* from the contract:

```text
 body (WHAT)            options (contract)                engine (HOW, derived)
─────────────────       ──────────────────────────        ─────────────────────────────
File/Cron/...    +     WhenHostnameContains("x")   →    when_begin recipe evaluated
                                                              per destination host
                    +   Privileged()                →    split apply: plain chunk +
                                                              doas/sudo gonf apply chunk
```

Two consequences:

1. **Options must attach at registration time.** `Privileged()` stamps every op
   the body will record with `elevate`, and `When*` predicates wrap the body's
   ops in a recipe — both must be known *before* the body runs. That is why
   `Task(..., opts...)` takes them beside the body, and why `RegisterMethods`
   needs the `OptsFoo()` companion to express them per method.
2. **Go control flow inside the body does not travel in the plan.** Only
   resource ops and `when_*` recipes are serialized. A gate written as Go
   (`if facts.Hostname ... { ... }`) executes on the *controller* during
   recording — the wrong host — so gated resources may never be recorded at
   all. Use the serializable `When*` options; they are evaluated on the
   destination at apply time.

Body-level recipe helpers (`WhenPathExists`, `WhenHostname`) make one task
carry several host- or path-gated fragments — the DRY fleet pattern: record
once, evaluate per destination. See [plan.md](plan.md) for the
recording/apply lifecycle, and its *Privilege (Task mark + Host helper)*
section for the chunk-split mechanics (when-blocks containing elevated ops
are promoted as a whole, so body-level gates compose with `Privileged()`).

## RegisterMethods

Reflect over exported methods on a struct. Companion methods:

- `Opts() TaskOptions` — struct-level **default** `TaskOption`s for every
  method registered from this struct (e.g. a single `Privileged()` for an
  all-privileged struct). A method's own `OptsFoo()` companion **adds to**
  the default for that method; `Unprivileged()` opts it out of
  `Privileged()` (e.g. an unprivileged smoke-test task on an
  otherwise-privileged struct). Composition order: `WithGroupWhen` options
  first, then the struct default, then the method's own options. (Before
  v0.22.0 `OptsFoo()` replaced the default, so every `OptsFoo()` of a
  `RequiresRoot` struct had to repeat `Privileged()`; forgetting it quietly
  dropped root.) The exact name `Opts` is
  reserved as this companion (a method named `Opts` is no longer
  registered as a task).
- **Embedded `StructOption` markers** — the declaration-site form of the
  same contract: embed a type implementing `StructOption` (method
  `StructTaskOptions() TaskOptions`) and its options become the struct
  default. gonf ships `RequiresRoot` for the common case:

  ```go
  type Unattended struct {
      RequiresRoot   // every task of this struct applies as root
  }
  ```

  Markers compose with `Opts()` and with a method's own `OptsFoo()`. Embed exported marker types; custom markers: define any
  type implementing `StructOption` and embed it.
- `DescFoo() string` — description for `-list`
- `WhenFoo() TaskOption` — per-method guard such as `WhenLinux()`; a
  serializable guard is evaluated on the destination, so the task still
  pushes. `WhenFoo(Facts) bool` is the older opaque form: controller only,
  and push, cluster and fleet refuse the task.
- `OptsFoo() TaskOptions` — per-method `TaskOption`s such as `Privileged()`
  or the serializable `WhenHostnameContains()` / `WhenProfile()`
  predicates; appended after any `WithGroupWhen` options of the same call.
  (`TaskOptions` is an alias for `[]TaskOption` — either form works.)
  A wrong signature is registration-time misuse: a declaration error is
  reported and that method's task is not registered (a silently ignored
  companion could drop `Privileged()` and lower a task's privileges).

```go
type Home struct{}

func (Home) DescHelix() string { return "Install helix" }
func (Home) Helix() { /* resources */ }

// Per-task options that RegisterMethods could not otherwise express:
func (Home) OptsPkgOpenBSD() TaskOptions { return TaskOptions{Privileged()} }
func (Home) WhenPkgOpenBSD() TaskOption  { return WhenOpenBSD() }
func (Home) PkgOpenBSD() { Package("git") }
// Hostname-gated and privileged, plan-serializable (when_begin recipe):
func (Home) OptsCronBlowfish() TaskOptions {
    return TaskOptions{Privileged(), WhenHostnameContains("blowfish")}
}
func (Home) CronBlowfish() { Cron(..., WithCommand(...)) }

RegisterMethods(Home{}, WithPrefix("home."), WithGroupWhen(WhenLinux()))
```

Name methods for the *action*, not the type: `Unattended.Script`, not
`Unattended.UnattendedScript` — the prefix already provides the namespace.

Without `WithPrefix`, `RegisterMethods` derives the prefix from the struct
(`DefaultPrefix`): package and type name in snake_case, a trailing `Tasks`
dropped, a type named like its package and package `main` omitted, so
`freebsd.Unattended` registers `freebsd_unattended_script` and `home.HomeTasks`
registers `home_helix`. The
package name is part of it because same-named structs of different packages
(`openbsd.Unattended`, `freebsd.Unattended`) would otherwise collide in the
one global task list. `WithPrefix` replaces the default when several structs
share one namespace; `WithPrefix("")` registers bare method names.

`WithPrefix` namespaces task names; `WithGroupWhen` takes `TaskOption`s
such as `WhenLinux()` / `WhenProfile(...)` so plan recording can emit
`when_begin` recipes; `OptsFoo` companions add per-method `TaskOption`s
without falling back to explicit `Task(...)` registration.

## Aggregates

```go
// Pattern membership: every activated task matching the regex, sorted by name.
Aggregate("home", "Install all home_* configuration", "^home_")

// Explicit membership: the listed tasks, in the listed order.
AggregateTasks("frontends", "Install all frontend configuration",
    "frontends_base", "frontends_httpd", "frontends_relayd")
```

An aggregate is a task that records its members into the same plan (nested
recording merges child ops; nothing is applied mid-flight). Both forms share
these rules:

- **Deduplication** is per *aggregate tree*: an aggregate, the aggregates
  nested in it, and the aliases they list. Within one tree a task is recorded
  **once**, at the first position it is reached — whether it is reached by
  name, through an alias, or through two nested aggregates. The tree stops at
  an ordinary task body: an aggregate that a plain task runs (`Run("inner")`)
  starts a fresh tree, because that task may add its own `When*` or
  `Privileged()` envelope, so its members are recorded again inside it.
  Separate top-level names (`gonf plan a b`) and diamonds between plain task
  bodies are not deduplicated, as before. A member that is still being
  recorded is a cycle and fails the record instead of being skipped.
- **Aliases** stay visible: members are recorded under their public names,
  so a cycle error names the alias in the chain.
- **Conditions** stay the members' own: a member whose opaque `When`
  predicate excludes it on the controller is skipped (the same activation
  filter as `-list`); a serializable guard never drops a member, it becomes
  the member's `when_begin` recipe, evaluated per destination (see
  *Destination guards* above; a local run skips a member whose guard does
  not hold on this host). Privilege chunks (`Privileged()`) are per member,
  as for a direct run.

  The push consequence (approved behaviour change, 2026-09-24, task 8h2):
  before 8h2, `gonf push paul@rocky home` run from the controller earth
  silently dropped dotfiles' `home_tmux_rocky`, because its
  `WhenHostnameContains("rocky")` was evaluated on the controller, where it
  fails — although naming the task explicitly recorded it with its
  `when_begin` and it applied on rocky (dotfiles worked around it on branch
  `e2e-tmux-rocky`, commit b092675). Now the guard is evaluated on the
  destination: `home` records `home_tmux_rocky` inside
  `when_begin{hostname_contains: rocky}` on every controller, rocky applies
  it, and every other destination skips it. Likewise a
  `WithGroupWhen(WhenProfile("fedora"))` group joins a pattern aggregate
  pushed from a rocky controller, guarded by the profile.
- **Errors propagate**: a member that fails to record, a broken alias, or an
  empty member set fails the whole record with the aggregate chain in the
  message (`aggregate outer: aggregate inner: …`). Cycles are detected across
  aggregates and aliases.

`Aggregate` excludes itself (by name or through an alias of it) and all
*operational work*: an `Operational()` task, an alias of one, and an
`AggregateTasks` that lists one at any depth (whether or not that member is
active on the controller). A broad pattern therefore cannot pick an explicit
operational action up by its registration, not even wrapped in another
aggregate. Such an `AggregateTasks` is dropped from the pattern silently (run
with `-verbose` to see a debug line naming it); list it explicitly if the
pattern aggregate should run it. Task *bodies* are not inspected: an ordinary
task whose body calls `Run("op")` records `op` wherever that task is
recorded, pattern aggregates included, so never wrap an operational action in
a plain task that a setup pattern matches.

Prefer `AggregateTasks` when membership is a safety decision — a setup
aggregate that must leave certificate requests, one-shot invocations and
diagnostics out — instead of encoding that policy in a growing regex. Members
may be tasks, aliases or other aggregates; an `Operational()` member is
allowed because listing it is explicit (which makes the aggregate operational
work for pattern aggregates). A member name that is not registered fails the
record (a typo must not shrink a setup run silently); an empty list, an empty
or duplicate member, or the aggregate listing itself — by name or through an
alias of it, in either registration order — are declaration errors at
registration (the aggregate is not registered; the run is refused).

## Alias

```go
Alias("home_prompts", "Legacy alias for home_agents", "home_agents")
```

Registers a second public name for a task — typically a legacy name kept for
compatibility. The alias is accepted everywhere a task name is (`Run`, the CLI,
`plan`, `push`, `cluster`, `fleet`) and records exactly the target's ops,
with the target's conditions, privilege and cluster; it has no options of its
own. It is listed under its own name and description (or `alias of <target>`
when the description is empty) whenever the target is active. Unlike a task
whose body only calls `Run(target)`, an alias does not make an aggregate tree
record the target twice.

The target may be registered before or after the alias; it is resolved when a
plan is recorded. An unknown target, or a target that is itself an alias, fails
that record (and such an alias is never listed). Duplicate names, empty names
or targets, self-aliases, and an alias of an `AggregateTasks` that lists the
alias are declaration errors at registration (the alias is not registered; the
run is refused).

## Operational tasks

```go
Task("frontends_acme_invoke", "Request certificates now", requestCerts, Operational())
```

`Operational()` marks an explicit action — certificate issuance, a one-shot
invocation, a diagnostic — as opposed to configuration convergence. It never
joins a pattern `Aggregate`, directly, through an alias, or inside an
`AggregateTasks`; run it by name or list it in an `AggregateTasks`. The
marker is checked on registrations only, not on what task bodies `Run` (see
above).

## Nested Run

A task body may call `Run("other")` while a plan is being recorded; the other
task's ops are appended to the same plan. A nested failure is returned to the
body **and** fails the enclosing record (`task <body>: …`), so `_ = Run(...)`
cannot silently drop ops. This holds even when the body inspects and handles
the returned error (for example to fall back to another task): a nested
failure is never recoverable inside a recording, so optional work must be
decided before calling `Run` (e.g. with a serializable `When*` option on the
child), not by trying and ignoring the error. Use `Alias` instead of a body
that only runs another task.

## Facts

```go
type Facts struct {
    Profile  string // fedora | rocky | os-release ID | override
    GOOS     string
    Hostname string
}
```

Built by `DetectFacts()`. Profile comes from hostname heuristics / `/etc/os-release`,
or `-profile=...` / `SetProfileOverride`. Used by activation (opaque
predicates, and the `-list` destination-guarded mark), by a local run's
aggregate membership, and by `ApplyPlan` when interpreting `when_begin` fact
predicates.

`ProfileIs("fedora")` is a ready-made predicate for custom `When` / `And` / `Or`.

## CLI

```go
func main() { os.Exit(cli.CLI()) }
```

| Invocation | Meaning |
|------------|---------|
| `-list` | Print activated tasks; a task whose serializable guard does not hold here is marked `[destination-guarded: <guard>]` |
| `-version` | Print library version |
| `-profile=` | Override Facts.Profile |
| `-dry-run` / `-n` | Preview without mutating |
| `-verbose` / `-quiet` | Log level |
| `<task>…` | **RecordPlan + Apply** locally |
| `plan [-o dir] [-id name] <task>…` | Write `plan.jsonl` (+ blobs) only, or a sealed `plan.age` (`-seal`, or by default for a plan carrying secrets when a recipients file exists) |
| `apply [-n] <plan.jsonl\|plan.age>` | Apply a plan file (sealed and signed ones too) |

The full command and flag list (push, cluster, fleet, sealing, signing,
`plan-verify`, `plan-signer-keygen`) is in [plan.md](plan.md), "CLI".

Each `CLI()` call scopes the settings its flags change — dry-run, log level,
`-privilege`, `-profile` and `-cmd-timeout` — to that call and restores them
when it returns (tasks vg2, xg2), so a program that calls `CLI()` more than
once, or calls `Run` / `Apply` after it, never inherits an earlier
invocation's `-n` or `-privilege`.

`Activate(DetectFacts())` runs inside `CLI` so `-list` and profile overrides
see the final Facts. Task **execution** still records candidates with their
`When*` recipes rather than dropping tasks at activation time, and
activation drops only tasks whose opaque `When` fails (see *Destination
guards*).

## Programmatic run

```go
// Preferred: same engine as the CLI (plan → apply).
if err := Run("home.helix"); err != nil { /* … */ }

ops, err := RecordPlan("id", planDir, "home.helix")
if err != nil { /* … */ }
if err := ApplyPlan(ops, planDir); err != nil { /* … */ }
```

`Matching("home\\..*")` returns activated task names matching a regex,
aliases, operational and destination-guarded tasks included (`Aggregate`
filters and resolves them afterwards). `Activate` only filters the list for
display / matching by opaque predicates — it does not apply configuration by
itself. `Tasks()` reports the destination-guarded mark as
`TaskInfo.DestinationGuard`.

`api.Apply()` snapshots registered drafts and uses the plan engine (the
direct `resource.Apply()` repository path was retired in task e72),
including the privilege split: a `WithElevate` command runs through the
configured `-privilege` helper like a `Privileged()` task under `Run`. See
[plan.md](plan.md) ("Low-level `Apply()`").
