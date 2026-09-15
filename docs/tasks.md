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

Combine fact predicates with `And` / `Or` from [helpers.md](helpers.md) for
custom `When` only — prefer the named helpers when you need remote plans.

## RegisterMethods

Reflect over exported methods on a struct. Companion methods:

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

Low-level `Apply()` (register resources, then `resource.Apply`) remains for
tests and ad-hoc use; see [plan.md](plan.md).
