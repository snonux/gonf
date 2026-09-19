# Tasks, Facts, and CLI

gonf configs are Go programs: register **tasks**, then run them via `cli.CLI()` or
`Run(...)`. Execution always goes through the **plan → apply** engine — see
[plan.md](plan.md).

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
  all-privileged struct). A method's own `OptsFoo()` companion **replaces**
  the default for that method — an empty `TaskOptions` opts out (e.g. an
  unprivileged smoke-test task on an otherwise-privileged struct).
  Composition order: `WithGroupWhen` options first, then the struct
  default (or the method's replacement). The exact name `Opts` is
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

  Markers compose with `Opts()` and are replaced by a method's own
  `OptsFoo()`. Embed exported marker types; custom markers: define any
  type implementing `StructOption` and embed it.
- `DescFoo() string` — description for `-list`
- `WhenFoo(Facts) bool` — per-method filter (opaque unless you also use
  serializable `TaskOption`s via `WithGroupWhen`)
- `OptsFoo() TaskOptions` — per-method `TaskOption`s such as `Privileged()`
  or the serializable `WhenHostnameContains()` / `WhenProfile()`
  predicates; appended after any `WithGroupWhen` options of the same call.
  (`TaskOptions` is an alias for `[]TaskOption` — either form works.)
  A wrong signature is registration-time misuse and panics (a silently
  ignored companion could drop `Privileged()` and lower a task's
  privileges).

```go
type Home struct{}

func (Home) DescHelix() string { return "Install helix" }
func (Home) Helix() { /* resources */ }

// Per-task options that RegisterMethods could not otherwise express:
func (Home) OptsPkgOpenBSD() TaskOptions { return TaskOptions{Privileged()} }
func (Home) PkgOpenBSD() { Package("git") }
// Hostname-gated and privileged, plan-serializable (when_begin recipe):
func (Home) OptsCronBlowfish() TaskOptions {
    return TaskOptions{Privileged(), WhenHostnameContains("blowfish")}
}
func (Home) CronBlowfish() { Cron(..., WithCommand(...)) }

RegisterMethods(Home{}, WithPrefix("home."), WithGroupWhen(WhenLinux()))
```

Name methods for the *action*, not the type: `Unattended.Script`, not
`Unattended.UnattendedScript` — `WithPrefix` and the receiver type already
provide the namespace.

`WithPrefix` namespaces task names; `WithGroupWhen` takes `TaskOption`s
such as `WhenLinux()` / `WhenProfile(...)` so plan recording can emit
`when_begin` recipes; `OptsFoo` companions add per-method `TaskOption`s
without falling back to explicit `Task(...)` registration.

## Aggregate

```go
Aggregate("all", "Everything matching home.*", "home\\..*")
```

Registers a task that `Run`s every activated task whose name matches the regex.
Nested `Run` while recording merges child ops into the same plan.

## Facts

```go
type Facts struct {
    Profile  string // fedora | rocky | os-release ID | override
    GOOS     string
    Hostname string
}
```

Built by `DetectFacts()`. Profile comes from hostname heuristics / `/etc/os-release`,
or `-profile=...` / `SetProfileOverride`. Used by `-list` activation and by
`ApplyPlan` when interpreting `when_begin` fact predicates.

`ProfileIs("fedora")` is a ready-made predicate for custom `When` / `And` / `Or`.

## CLI

```go
func main() { os.Exit(cli.CLI()) }
```

| Invocation | Meaning |
|------------|---------|
| `-list` | Print activated tasks (display filter via Facts) |
| `-version` | Print library version |
| `-profile=` | Override Facts.Profile |
| `-dry-run` / `-n` | Preview without mutating |
| `-verbose` / `-quiet` | Log level |
| `<task>…` | **RecordPlan + Apply** locally |
| `plan [-o dir] [-id name] <task>…` | Write `plan.jsonl` (+ blobs) only |
| `apply [-n] <plan.jsonl>` | Apply a plan file |

`Activate(DetectFacts())` runs inside `CLI` so `-list` and profile overrides
see the final Facts. Task **execution** still records candidates with their
`When*` recipes rather than dropping tasks at activation time.

## Programmatic run

```go
// Preferred: same engine as the CLI (plan → apply).
if err := Run("home.helix"); err != nil { /* … */ }

ops, err := RecordPlan("id", planDir, "home.helix")
if err != nil { /* … */ }
if err := ApplyPlan(ops, planDir); err != nil { /* … */ }
```

`Matching("home\\..*")` returns activated task names matching a regex
(used by `Aggregate`). `Activate` only filters the list for display /
matching — it does not apply configuration by itself.

Low-level `resource.Apply()` remains for tests and ad-hoc compatibility use;
`api.Apply()` now snapshots registered drafts and uses the plan engine. See
[plan.md](plan.md).
